// Package controller implements the VibedApp reconciler.
//
// The reconciler owns the state machine from refactor.md §5.3:
//
//	Pending → Claiming → Starting → Ready
//	                              ↘ Suspended (after idle TTL, milestone F2)
//	                              ↘ Failed
//
// Milestone B1 lands the skeleton: spec validation, phase transitions,
// condition management, and Status patching. The real side-effects —
// creating SandboxClaim CRs, talking to vibed-agent's /ready, writing routes
// to vibed-router — are abstracted behind the Claimer and AgentProbe
// interfaces so later milestones can fill them in without touching the
// reconcile loop. Default implementations are stubs that return immediately.
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// Condition types written into VibedAppStatus.Conditions.
const (
	// ConditionReady is the headline condition: True once the app is
	// serving traffic, False otherwise.
	ConditionReady = "Ready"

	// ConditionSourceValid is True when the spec's Source resolves to
	// exactly one of TarballRef or GitRef. Provides the user-visible reason
	// when validation fails.
	ConditionSourceValid = "SourceValid"
)

// Reason strings used in conditions. Keep short and machine-readable.
const (
	ReasonClaiming        = "Claiming"
	ReasonStarting        = "Starting"
	ReasonRunning         = "Running"
	ReasonSourceMissing   = "SourceMissing"
	ReasonSourceAmbiguous = "SourceAmbiguous"
	ReasonClaimFailed     = "ClaimFailed"
	ReasonAgentUnreachable = "AgentUnreachable"
)

// Claimer obtains a Sandbox for a VibedApp. Milestone C plugs in the warm
// pool implementation; for B1 the default DummyClaimer immediately returns a
// fixed sandboxRef so the state machine progresses.
type Claimer interface {
	// Claim returns a stable reference (e.g. agents.x-k8s.io Sandbox name)
	// for the given app. Implementations are expected to be idempotent: if
	// a claim already exists for app, return its ref.
	Claim(ctx context.Context, app *vibedv1.VibedApp) (sandboxRef string, err error)
}

// AgentProbe checks whether vibed-agent inside sandboxRef has finished
// preparing the workspace and reports the user process is listening.
// Milestone B2 swaps in the real HTTP probe; the default DummyAgentProbe
// always returns ready=true.
type AgentProbe interface {
	IsReady(ctx context.Context, sandboxRef string) (ready bool, err error)
}

// Router publishes a route for a Ready app and returns the externally
// resolvable URL. Milestone D wires Caddy via vibed-router; the default
// DummyRouter synthesizes a URL from app ID + domain.
type Router interface {
	Publish(ctx context.Context, app *vibedv1.VibedApp, sandboxRef string) (url string, err error)
}

// Reconciler reconciles VibedApp resources.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Claimer Claimer
	Probe   AgentProbe
	Router  Router

	// RequeueDelay is how often to re-check transitional phases when no
	// external signal triggers a reconcile. Defaults to 2s.
	RequeueDelay time.Duration
}

// SetupWithManager registers the reconciler with the manager. Watching the
// Sandbox CR (so claims/probes can trigger reconciles without polling) lands
// in milestone C — for now we own only VibedApp.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Claimer == nil {
		r.Claimer = DummyClaimer{}
	}
	if r.Probe == nil {
		r.Probe = DummyAgentProbe{}
	}
	if r.Router == nil {
		r.Router = DummyRouter{Domain: "vibed.example.com"}
	}
	if r.RequeueDelay == 0 {
		r.RequeueDelay = 2 * time.Second
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&vibedv1.VibedApp{}, builder.WithPredicates()).
		Named("vibedapp").
		Complete(r)
}

// applyDefaults lazily fills in stub Claimer/Probe/Router and a non-zero
// RequeueDelay so unit tests that construct Reconciler directly (bypassing
// SetupWithManager) still behave correctly.
func (r *Reconciler) applyDefaults() {
	if r.Claimer == nil {
		r.Claimer = DummyClaimer{}
	}
	if r.Probe == nil {
		r.Probe = DummyAgentProbe{}
	}
	if r.Router == nil {
		r.Router = DummyRouter{Domain: "vibed.example.com"}
	}
	if r.RequeueDelay == 0 {
		r.RequeueDelay = 2 * time.Second
	}
}

// Reconcile drives the state machine forward by one step.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.applyDefaults()
	logger := log.FromContext(ctx).WithValues("vibedapp", req.NamespacedName)

	var app vibedv1.VibedApp
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		// Either deleted or transient API error — ignore-not-found and let
		// controller-runtime retry the latter with backoff.
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Don't reconcile objects that have been deleted; the controller does
	// no finalizer-protected cleanup in B1 (the only K8s side-effect is the
	// Sandbox CR, which agent-sandbox cleans up via ownerRefs when wired in
	// milestone C).
	if app.DeletionTimestamp != nil {
		return reconcile.Result{}, nil
	}

	// Snapshot status so we only patch if something changed.
	before := app.Status.DeepCopy()

	// Validation gate. Failure here is terminal until the user updates spec.
	if err := validateSpec(&app.Spec); err != nil {
		setCondition(&app, ConditionSourceValid, metav1.ConditionFalse, reasonFromErr(err), err.Error())
		setCondition(&app, ConditionReady, metav1.ConditionFalse, reasonFromErr(err), "spec is invalid")
		app.Status.Phase = vibedv1.PhaseFailed
		if err := r.patchStatusIfChanged(ctx, &app, before); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}
	setCondition(&app, ConditionSourceValid, metav1.ConditionTrue, "Valid", "")

	// Phase transitions. Each case is a single hop; we requeue rather than
	// loop here so each step is observable in status before the next runs.
	switch app.Status.Phase {
	case "", vibedv1.PhasePending:
		app.Status.Phase = vibedv1.PhaseClaiming
		setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonClaiming, "waiting on warm pool")
		return r.finish(ctx, &app, before, true)

	case vibedv1.PhaseClaiming:
		sandboxRef, err := r.Claimer.Claim(ctx, &app)
		if err != nil {
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonClaimFailed, err.Error())
			logger.Info("claim failed; will retry", "error", err)
			if perr := r.patchStatusIfChanged(ctx, &app, before); perr != nil {
				return reconcile.Result{}, perr
			}
			return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		app.Status.SandboxRef = sandboxRef
		app.Status.Phase = vibedv1.PhaseStarting
		setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonStarting, "waiting for agent")
		return r.finish(ctx, &app, before, true)

	case vibedv1.PhaseStarting:
		ready, err := r.Probe.IsReady(ctx, app.Status.SandboxRef)
		if err != nil {
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonAgentUnreachable, err.Error())
			if perr := r.patchStatusIfChanged(ctx, &app, before); perr != nil {
				return reconcile.Result{}, perr
			}
			return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		if !ready {
			// Agent reachable but user process not yet listening — requeue
			// without rewriting status (nothing changed).
			return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		url, err := r.Router.Publish(ctx, &app, app.Status.SandboxRef)
		if err != nil {
			setCondition(&app, ConditionReady, metav1.ConditionFalse, "RouterFailed", err.Error())
			if perr := r.patchStatusIfChanged(ctx, &app, before); perr != nil {
				return reconcile.Result{}, perr
			}
			return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		app.Status.URL = url
		app.Status.Phase = vibedv1.PhaseReady
		now := metav1.Now()
		app.Status.LastDeployedAt = &now
		setCondition(&app, ConditionReady, metav1.ConditionTrue, ReasonRunning, "user process listening")
		return r.finish(ctx, &app, before, false)

	case vibedv1.PhaseReady, vibedv1.PhaseSuspended, vibedv1.PhaseFailed:
		// Steady states: nothing to do until spec changes or an external
		// signal (idle TTL → Suspended, milestone F2) flips us.
		return reconcile.Result{}, nil
	}

	logger.Info("unknown phase; treating as Pending", "phase", app.Status.Phase)
	app.Status.Phase = vibedv1.PhasePending
	return r.finish(ctx, &app, before, true)
}

// finish patches the status if anything changed and decides whether to
// requeue. requeue=true means we're mid-state-machine and a follow-up
// reconcile is needed even if nothing external pings us.
func (r *Reconciler) finish(ctx context.Context, app *vibedv1.VibedApp, before *vibedv1.VibedAppStatus, requeue bool) (reconcile.Result, error) {
	if err := r.patchStatusIfChanged(ctx, app, before); err != nil {
		return reconcile.Result{}, err
	}
	if requeue {
		return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
	}
	return reconcile.Result{}, nil
}

func (r *Reconciler) patchStatusIfChanged(ctx context.Context, app *vibedv1.VibedApp, before *vibedv1.VibedAppStatus) error {
	if statusEqual(before, &app.Status) {
		return nil
	}
	// Build the patch by re-fetching the latest object and updating its
	// status subresource. Using Status().Update keeps us safe against
	// spec-side changes racing with reconciles.
	return r.Status().Update(ctx, app)
}

// statusEqual is a cheap "did anything I care about change?" check. We
// can't reflect.DeepEqual because conditions carry timestamps that we
// rewrite even when nothing else moved; instead compare the load-bearing
// fields explicitly.
func statusEqual(a, b *vibedv1.VibedAppStatus) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Phase != b.Phase || a.URL != b.URL || a.SandboxRef != b.SandboxRef ||
		a.SnapshotRef != b.SnapshotRef || !timePtrEqual(a.LastDeployedAt, b.LastDeployedAt) {
		return false
	}
	if len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		if a.Conditions[i].Type != b.Conditions[i].Type ||
			a.Conditions[i].Status != b.Conditions[i].Status ||
			a.Conditions[i].Reason != b.Conditions[i].Reason ||
			a.Conditions[i].Message != b.Conditions[i].Message {
			return false
		}
	}
	return true
}

func timePtrEqual(a, b *metav1.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(b)
}

// validateSpec is the gate that decides whether a VibedApp can be reconciled
// at all. Failure here flips the app into Failed and surfaces SourceValid=False.
func validateSpec(spec *vibedv1.VibedAppSpec) error {
	hasTarball := spec.Source.TarballRef != ""
	hasGit := spec.Source.GitRef != ""
	switch {
	case !hasTarball && !hasGit:
		return errors.New("spec.source must set tarballRef or gitRef")
	case hasTarball && hasGit:
		return errors.New("spec.source must set exactly one of tarballRef or gitRef")
	}
	if spec.Runtime.Lane == "" {
		return errors.New("spec.runtime.lane is required")
	}
	return nil
}

func reasonFromErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "exactly one"):
		return ReasonSourceAmbiguous
	default:
		return ReasonSourceMissing
	}
}

// setCondition merges a condition entry by type. New entries get
// LastTransitionTime=now; existing entries only update LastTransitionTime if
// the Status flipped.
func setCondition(app *vibedv1.VibedApp, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range app.Status.Conditions {
		if c.Type != condType {
			continue
		}
		if c.Status != status {
			app.Status.Conditions[i].LastTransitionTime = now
		}
		app.Status.Conditions[i].Status = status
		app.Status.Conditions[i].Reason = reason
		app.Status.Conditions[i].Message = message
		return
	}
	app.Status.Conditions = append(app.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

// ---- Default stub implementations (replaced in later milestones) ----

// DummyClaimer immediately returns a fake sandbox ref. Milestone C plugs in
// the real warm-pool-backed implementation.
type DummyClaimer struct{}

func (DummyClaimer) Claim(_ context.Context, app *vibedv1.VibedApp) (string, error) {
	return fmt.Sprintf("dummy-sandbox-%s", app.UID), nil
}

// DummyAgentProbe always reports ready. Milestone B2 swaps in an HTTP probe
// against vibed-agent's /ready endpoint.
type DummyAgentProbe struct{}

func (DummyAgentProbe) IsReady(_ context.Context, _ string) (bool, error) { return true, nil }

// DummyRouter synthesizes a URL of the form
// `https://<name>-<short-uid>.<domain>`. Milestone D wires Caddy.
type DummyRouter struct{ Domain string }

func (d DummyRouter) Publish(_ context.Context, app *vibedv1.VibedApp, _ string) (string, error) {
	domain := d.Domain
	if domain == "" {
		domain = "vibed.example.com"
	}
	shortUID := string(app.UID)
	if len(shortUID) > 8 {
		shortUID = shortUID[:8]
	}
	return fmt.Sprintf("https://%s-%s.%s", app.Name, shortUID, domain), nil
}
