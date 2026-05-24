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
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
	ReasonClaiming         = "Claiming"
	ReasonStarting         = "Starting"
	ReasonRunning          = "Running"
	ReasonSourceMissing    = "SourceMissing"
	ReasonSourceAmbiguous  = "SourceAmbiguous"
	ReasonClaimFailed      = "ClaimFailed"
	ReasonAgentUnreachable = "AgentUnreachable"
	ReasonInjectFailed     = "InjectFailed"
	ReasonServiceFailed    = "ServiceFailed"
	ReasonTemplateInvalid  = "TemplateInvalid"
	ReasonTemplateMissing  = "TemplateMissing"
	ReasonSuspended        = "Suspended"
)

// Claimer obtains a Sandbox for a VibedApp by creating (or reading) a
// SandboxClaim against the configured warm pool. EnsureClaim is the only
// method and is expected to be idempotent: calling it repeatedly for the
// same app returns the same claim and surfaces the current binding state.
//
// The bound==false return value is a normal first-class outcome — it means
// agent-sandbox hasn't bound a pod yet, not that anything went wrong. The
// reconciler requeues and tries again. err is reserved for real failures
// (transient API errors, etc.).
type Claimer interface {
	EnsureClaim(ctx context.Context, app *vibedv1.VibedApp) (bound bool, sandboxRef string, podIP string, err error)
	// ReleaseClaim deletes the app's SandboxClaim, returning its bound pod to
	// the warm pool. Used to suspend an app (free its compute). Idempotent: a
	// missing claim is not an error.
	ReleaseClaim(ctx context.Context, app *vibedv1.VibedApp) error
}

// AgentProbe checks whether vibed-agent at target ("host:port") has
// finished preparing the workspace and reports the user process is
// listening. Milestone C3 swaps in the real HTTP probe; the default
// DummyAgentProbe always returns ready=true.
type AgentProbe interface {
	IsReady(ctx context.Context, target string) (ready bool, err error)
}

// Injector pushes the user's source into a bound sandbox's vibed-agent
// (refactor.md §5.3 step 2): once the agent at target ("host:port") is
// healthy, the controller POSTs the source tarball URL so the agent pulls
// it into /workspace and starts the user process. Returns running=true once
// the process is up. The default DummyInjector reports running without a
// real agent (tests / no-op); the real HTTPInjector lives in inject.go.
type Injector interface {
	Inject(ctx context.Context, target string, app *vibedv1.VibedApp) (running bool, err error)
}

// FastLaneDeployer deploys a workerd fast-lane app: it pushes the worker
// script to the loader (no warm-pool claim, no microVM) and returns the
// in-cluster target ("host:port") the worker is reachable at. Remove tears
// it back down. The default DummyFastLaneDeployer errors, since the fast
// lane must be explicitly wired (cmd/vibed-controller supplies the real
// workerd-backed implementation).
type FastLaneDeployer interface {
	Deploy(ctx context.Context, app *vibedv1.VibedApp) (target string, err error)
	Remove(ctx context.Context, app *vibedv1.VibedApp) error
}

// Router returns the externally resolvable URL for a Ready app. The
// default DeterministicRouter computes it from a stable hash of
// namespace/name with no side effects — vibed-router is the separate
// component that observes VibedApp.status.url and reconciles the actual
// Caddy route, so the controller's job here is to publish what the URL
// *will be*, not to make it routable.
type Router interface {
	Publish(ctx context.Context, app *vibedv1.VibedApp, sandboxRef string) (url string, err error)
}

// Reconciler reconciles VibedApp resources.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Claimer  Claimer
	Probe    AgentProbe
	Injector Injector
	Services ServiceManager
	Router   Router
	FastLane FastLaneDeployer

	// Gate hard-gates claims on bring-your-own base-image validation; the
	// default allows everything (no validation wired).
	Gate TemplateGate

	// RequeueDelay is how often to re-check transitional phases when no
	// external signal triggers a reconcile. Defaults to 2s.
	RequeueDelay time.Duration
}

// SetupWithManager registers the reconciler with the manager. The
// SandboxClaim watch ensures status changes on the claim
// (agent-sandbox binding a pod, the pod becoming Ready) wake the
// VibedApp reconciler immediately instead of relying on RequeueAfter
// polling. The mapping is name-based: SandboxClaim and its owning
// VibedApp share namespace/name by convention (see SandboxClaimer).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.applyDefaults()

	claimGVKType := &unstructured.Unstructured{}
	claimGVKType.SetGroupVersionKind(SandboxClaimGVK)

	return ctrl.NewControllerManagedBy(mgr).
		For(&vibedv1.VibedApp{}, builder.WithPredicates()).
		Watches(
			claimGVKType,
			handler.EnqueueRequestsFromMapFunc(claimToVibedApp),
		).
		Named("vibedapp").
		Complete(r)
}

// claimToVibedApp maps a SandboxClaim event to the same-name VibedApp
// in the same namespace. SandboxClaimer enforces this naming on creation.
func claimToVibedApp(_ context.Context, obj client.Object) []reconcile.Request {
	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}},
	}
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
	if r.Injector == nil {
		r.Injector = DummyInjector{}
	}
	if r.Services == nil {
		r.Services = DummyServiceManager{}
	}
	if r.Router == nil {
		r.Router = DeterministicRouter{Domain: "vibed.example.com"}
	}
	if r.FastLane == nil {
		r.FastLane = DummyFastLaneDeployer{}
	}
	if r.Gate == nil {
		r.Gate = AllowAllTemplateGate{}
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

	// Suspend/restore intent. spec.suspended is the declarative desired state:
	// when set, the backing compute is released and the app parked in
	// Suspended; clearing it re-enters the normal claim/deploy machine. Handled
	// before the lane split because both lanes can be suspended.
	if handled, res, err := r.reconcileSuspension(ctx, &app, before); handled {
		return res, err
	}

	// Fast lane (workerd): there's no warm-pool claim or agent — the loader
	// injects the worker script directly — so the whole lifecycle collapses
	// into one deploy step handled separately from the general-lane state
	// machine below.
	if isWorkerdFastLane(&app) {
		return r.reconcileFastLane(ctx, &app, before)
	}

	// Phase transitions. Each case is a single hop; we requeue rather than
	// loop here so each step is observable in status before the next runs.
	switch app.Status.Phase {
	case "", vibedv1.PhasePending:
		app.Status.Phase = vibedv1.PhaseClaiming
		setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonClaiming, "waiting on warm pool")
		return r.finish(ctx, &app, before, true)

	case vibedv1.PhaseClaiming:
		// Hard-gate on BYO base-image validation: refuse to claim a sandbox
		// whose image is known to have failed validation (wrong language /
		// missing runtime / no agent). Terminal — the user must fix the image
		// or the operator the slot config.
		if ok, reason := r.Gate.Allowed(ctx, app.Spec.Runtime.Template); !ok {
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonTemplateInvalid,
				fmt.Sprintf("template %q image failed validation: %s", app.Spec.Runtime.Template, reason))
			app.Status.Phase = vibedv1.PhaseFailed
			logger.Info("blocking claim: template image invalid", "template", app.Spec.Runtime.Template, "reason", reason)
			return r.finish(ctx, &app, before, false)
		}
		bound, sandboxRef, podIP, err := r.Claimer.EnsureClaim(ctx, &app)
		if errors.Is(err, ErrTemplateNotFound) {
			// No warm pool backs this slot — terminal, don't retry forever.
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonTemplateMissing, err.Error())
			app.Status.Phase = vibedv1.PhaseFailed
			logger.Info("no warm pool for template; failing", "template", app.Spec.Runtime.Template)
			return r.finish(ctx, &app, before, false)
		}
		if err != nil {
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonClaimFailed, err.Error())
			logger.Info("claim failed; will retry", "error", err)
			if perr := r.patchStatusIfChanged(ctx, &app, before); perr != nil {
				return reconcile.Result{}, perr
			}
			return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		if !bound {
			// SandboxClaim exists but agent-sandbox hasn't bound a pod yet.
			// Stay in Claiming; the Watches() on SandboxClaim wakes us when
			// it does, and the periodic requeue is a backstop.
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonClaiming, "waiting on warm pool")
			if perr := r.patchStatusIfChanged(ctx, &app, before); perr != nil {
				return reconcile.Result{}, perr
			}
			return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		app.Status.SandboxRef = sandboxRef
		app.Status.PodIP = podIP
		app.Status.Phase = vibedv1.PhaseStarting
		setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonStarting, "waiting for agent")
		return r.finish(ctx, &app, before, true)

	case vibedv1.PhaseStarting:
		target := app.Status.PodIP
		if target == "" {
			// Should not happen — Claiming → Starting only on bound claim.
			// Drop back to Claiming and let the next loop figure it out.
			app.Status.Phase = vibedv1.PhaseClaiming
			return r.finish(ctx, &app, before, true)
		}
		// vibed-agent's control API listens on :9000 inside the pod
		// (see templates/*/Dockerfile VIBED_AGENT_CONTROL_ADDR).
		target = target + ":9000"
		ready, err := r.Probe.IsReady(ctx, target)
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
		// Agent is up — inject the user's source (refactor.md §5.3 step 2).
		// The agent pulls the tarball from spec.source.tarballRef and starts
		// the user process. Idempotent: re-injecting on a requeue replaces
		// the running process.
		running, err := r.Injector.Inject(ctx, target, &app)
		if err != nil {
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonInjectFailed, err.Error())
			if perr := r.patchStatusIfChanged(ctx, &app, before); perr != nil {
				return reconcile.Result{}, perr
			}
			return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		if !running {
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonStarting, "user process starting")
			if perr := r.patchStatusIfChanged(ctx, &app, before); perr != nil {
				return reconcile.Result{}, perr
			}
			return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		// Publish a stable upstream for the router: a per-app Service that
		// re-selects the bound pod across restarts, so the route doesn't go
		// stale when the Sandbox pod's IP changes.
		routeTarget, err := r.Services.EnsureService(ctx, &app)
		if err != nil {
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonServiceFailed, err.Error())
			if perr := r.patchStatusIfChanged(ctx, &app, before); perr != nil {
				return reconcile.Result{}, perr
			}
			return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		app.Status.RouteTarget = routeTarget
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

	case vibedv1.PhaseReady:
		// Detect Sandbox pod recreation. The SandboxClaim watch wakes us when
		// agent-sandbox rebinds a replacement pod (new IP, fresh empty
		// workspace). The per-app Service already follows the new pod, so
		// routing stays valid — but the workspace lost the injected source, so
		// the user process won't be listening. Drop back to Starting to
		// re-probe + re-inject; the unchanged route serves again once it's up.
		bound, sandboxRef, podIP, err := r.Claimer.EnsureClaim(ctx, &app)
		if err == nil && bound && podIP != "" && podIP != app.Status.PodIP {
			logger.Info("sandbox pod replaced; re-injecting", "oldPodIP", app.Status.PodIP, "newPodIP", podIP)
			app.Status.PodIP = podIP
			app.Status.SandboxRef = sandboxRef
			app.Status.Phase = vibedv1.PhaseStarting
			setCondition(&app, ConditionReady, metav1.ConditionFalse, ReasonStarting, "sandbox pod replaced; re-injecting source")
			return r.finish(ctx, &app, before, true)
		}
		return reconcile.Result{}, nil

	case vibedv1.PhaseSuspended, vibedv1.PhaseFailed:
		// Steady states: nothing to do until spec changes or an external
		// signal (idle TTL → Suspended, milestone F2) flips us.
		return reconcile.Result{}, nil
	}

	logger.Info("unknown phase; treating as Pending", "phase", app.Status.Phase)
	app.Status.Phase = vibedv1.PhasePending
	return r.finish(ctx, &app, before, true)
}

// reconcileSuspension drives the declarative spec.suspended toggle. It returns
// handled=true (and the reconcile result) when it owns this pass:
//   - suspended and not yet Suspended → release the backing compute (the
//     SandboxClaim, or the workerd worker), clear the pod/route status, and
//     park in Suspended.
//   - suspended and already Suspended → steady state, nothing to do.
//   - not suspended but currently Suspended → flip to Claiming so the normal
//     machine re-claims a pod and re-injects source (restore).
//
// When the app is neither suspended nor currently Suspended it returns
// handled=false and the caller continues normal reconciliation.
func (r *Reconciler) reconcileSuspension(ctx context.Context, app *vibedv1.VibedApp, before *vibedv1.VibedAppStatus) (bool, reconcile.Result, error) {
	logger := log.FromContext(ctx)

	if app.Spec.Suspended {
		if app.Status.Phase == vibedv1.PhaseSuspended {
			return true, reconcile.Result{}, nil // already parked
		}
		// Release the backing compute. Lane-aware: workerd has a loader-managed
		// worker, the general lane a warm-pool SandboxClaim.
		var relErr error
		if isWorkerdFastLane(app) {
			relErr = r.FastLane.Remove(ctx, app)
		} else {
			relErr = r.Claimer.ReleaseClaim(ctx, app)
		}
		if relErr != nil {
			setCondition(app, ConditionReady, metav1.ConditionFalse, ReasonSuspended, "releasing compute: "+relErr.Error())
			if perr := r.patchStatusIfChanged(ctx, app, before); perr != nil {
				return true, reconcile.Result{}, perr
			}
			return true, reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
		}
		app.Status.PodIP = ""
		app.Status.SandboxRef = ""
		app.Status.RouteTarget = ""
		app.Status.Phase = vibedv1.PhaseSuspended
		setCondition(app, ConditionReady, metav1.ConditionFalse, ReasonSuspended, "app suspended; compute released")
		logger.Info("suspended app; released compute")
		res, err := r.finish(ctx, app, before, false)
		return true, res, err
	}

	// Not suspended. If we're parked in Suspended, restore by re-entering the
	// claim machine; otherwise let normal reconciliation proceed.
	if app.Status.Phase == vibedv1.PhaseSuspended {
		app.Status.Phase = vibedv1.PhaseClaiming
		setCondition(app, ConditionReady, metav1.ConditionFalse, ReasonClaiming, "restoring; re-claiming warm pool")
		logger.Info("restoring suspended app; re-claiming")
		res, err := r.finish(ctx, app, before, true)
		return true, res, err
	}

	return false, reconcile.Result{}, nil
}

// WorkerdTemplate is the template name the classifier emits for fast-lane
// worker scripts (worker.js / wrangler.toml). Kept in sync with
// internal/classifier.TemplateWorkerd.
const WorkerdTemplate = "workerd"

// isWorkerdFastLane reports whether the app takes the workerd fast lane
// (not the static-nginx fast lane, which still runs on a Sandbox pod).
func isWorkerdFastLane(app *vibedv1.VibedApp) bool {
	return app.Spec.Runtime.Lane == vibedv1.LaneFast && app.Spec.Runtime.Template == WorkerdTemplate
}

// reconcileFastLane drives a workerd app to Ready in a single step: push the
// script to the loader, record the returned target, publish the route. There's
// no Claiming/Starting — workerd is always running, so a successful deploy is
// immediately Ready.
func (r *Reconciler) reconcileFastLane(ctx context.Context, app *vibedv1.VibedApp, before *vibedv1.VibedAppStatus) (reconcile.Result, error) {
	// Steady state: nothing to do once Ready (a spec change bumps generation
	// and the loader Deploy below is idempotent, so re-running is harmless,
	// but we avoid the work when already serving).
	if app.Status.Phase == vibedv1.PhaseReady {
		return reconcile.Result{}, nil
	}

	target, err := r.FastLane.Deploy(ctx, app)
	if err != nil {
		app.Status.Phase = vibedv1.PhaseClaiming
		setCondition(app, ConditionReady, metav1.ConditionFalse, ReasonClaimFailed, err.Error())
		if perr := r.patchStatusIfChanged(ctx, app, before); perr != nil {
			return reconcile.Result{}, perr
		}
		return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	url, err := r.Router.Publish(ctx, app, target)
	if err != nil {
		setCondition(app, ConditionReady, metav1.ConditionFalse, "RouterFailed", err.Error())
		if perr := r.patchStatusIfChanged(ctx, app, before); perr != nil {
			return reconcile.Result{}, perr
		}
		return reconcile.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	// RouteTarget carries the routable upstream. For the fast lane it's the
	// workerd replica's host:port (the loader's assigned socket); there's no
	// Sandbox pod, so PodIP stays empty.
	app.Status.RouteTarget = target
	app.Status.URL = url
	app.Status.Phase = vibedv1.PhaseReady
	now := metav1.Now()
	app.Status.LastDeployedAt = &now
	setCondition(app, ConditionReady, metav1.ConditionTrue, ReasonRunning, "worker deployed")
	return r.finish(ctx, app, before, false)
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
		a.PodIP != b.PodIP || a.RouteTarget != b.RouteTarget ||
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

// DummyClaimer immediately returns a fake bound claim. Used in tests and as
// a stand-in until the real SandboxClaim-backed implementation is wired in
// vibed-controller main.
type DummyClaimer struct{}

func (DummyClaimer) EnsureClaim(_ context.Context, app *vibedv1.VibedApp) (bool, string, string, error) {
	return true, fmt.Sprintf("dummy-sandbox-%s", app.UID), "10.0.0.1", nil
}

func (DummyClaimer) ReleaseClaim(_ context.Context, _ *vibedv1.VibedApp) error { return nil }

// DummyAgentProbe always reports ready. The real HTTP probe lives in
// internal/controller/probe.go.
type DummyAgentProbe struct{}

func (DummyAgentProbe) IsReady(_ context.Context, _ string) (bool, error) { return true, nil }

// DummyInjector reports the user process as running without talking to a real
// agent. The real HTTP injector lives in internal/controller/inject.go.
type DummyInjector struct{}

func (DummyInjector) Inject(_ context.Context, _ string, _ *vibedv1.VibedApp) (bool, error) {
	return true, nil
}

// DummyFastLaneDeployer errors on use — the fast lane must be wired
// explicitly (cmd/vibed-controller supplies the workerd-backed impl). This
// default exists so a Reconciler constructed without one doesn't nil-panic;
// a workerd VibedApp simply fails to deploy with a clear message.
type DummyFastLaneDeployer struct{}

func (DummyFastLaneDeployer) Deploy(_ context.Context, _ *vibedv1.VibedApp) (string, error) {
	return "", errors.New("fast lane (workerd) is not configured on this controller")
}

func (DummyFastLaneDeployer) Remove(_ context.Context, _ *vibedv1.VibedApp) error { return nil }

// DeterministicRouter computes the user-facing URL for a VibedApp from a
// stable hash of namespace/name, with no side effects. The actual Caddy
// route is set up out-of-band by vibed-router watching VibedApp status —
// keeping this side-effect-free means the controller's reconcile stays
// fast (no waiting on Caddy admin API) and the same app always gets the
// same URL across restarts.
//
// Label shape: 12 lowercase alphanumeric characters, matching the
// refactor.md §5.5 Caddyfile regex `^[a-z0-9]{12}$`. The collision
// probability across 36^12 values is negligible at any plausible app count.
type DeterministicRouter struct {
	Domain string
	// Scheme is the URL scheme for published app URLs; defaults to "https".
	// Dev sets "http" because the dev Caddy serves plain HTTP.
	Scheme string
	// Port, when non-empty, is appended to the host (e.g. "18080"). Dev uses
	// it to point at a port-forwarded/NodePort Caddy; prod leaves it empty so
	// URLs are scheme-default (443/80).
	Port string
}

func (d DeterministicRouter) Publish(_ context.Context, app *vibedv1.VibedApp, _ string) (string, error) {
	domain := d.Domain
	if domain == "" {
		domain = "vibed.example.com"
	}
	scheme := d.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := AppLabel(app) + "." + domain
	if d.Port != "" {
		host += ":" + d.Port
	}
	return fmt.Sprintf("%s://%s", scheme, host), nil
}

// AppLabel is the 12-char DNS label vibed-router uses to identify the app
// in the Caddy route table. Exposed so the router can derive the same
// label from a VibedApp it observes — both sides must agree.
func AppLabel(app *vibedv1.VibedApp) string {
	sum := sha256.Sum256([]byte(app.Namespace + "/" + app.Name))
	// base32 hex (no padding) → 64-bit prefix → 13 chars, trim to 12.
	enc := base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:8])
	return strings.ToLower(enc[:12])
}
