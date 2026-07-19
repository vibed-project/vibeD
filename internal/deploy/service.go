// Package deploy is the service behind the /v1/deploy API: it turns an
// uploaded source tarball into a VibedApp CR and waits for it to become
// Ready. It composes the three primitives the deploy hot path needs — the
// classifier (lane/template), the tarball store (where the agent pulls
// source from), and the VibedApp CR client — so the HTTP handler stays a
// thin multipart parser.
package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/audit"
	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/authz"
	"github.com/vibed-project/vibeD/internal/classifier"
	"github.com/vibed-project/vibeD/internal/meter"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/policy"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/internal/tarball"
	"github.com/vibed-project/vibeD/internal/tenant"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// MaxTarballBytes caps the uploaded source at 50 MB (refactor.md §5.1).
const MaxTarballBytes = 50 * 1024 * 1024

var dnsName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Service coordinates a deploy. Safe for concurrent use.
type Service struct {
	Client     client.Client
	Store      tarball.Store
	Classifier classifier.Classifier

	// Clientset streams pod logs for StreamLogs (the controller-runtime client
	// can't read the pods/log subresource). nil disables log streaming.
	Clientset kubernetes.Interface

	// Namespace is where VibedApp CRs are created for the single-tenant default,
	// and the fallback namespace when a resolved tenant leaves it empty.
	Namespace string

	// Tenants resolves each request to its tenant (namespace + limits). nil means
	// the single-tenant default: every request runs in Namespace with no
	// per-tenant limits, so behavior is unchanged.
	Tenants tenant.Resolver
	// DeployTimeout bounds how long Deploy waits for Ready before returning
	// a still-pending result (the API turns this into a 202). Default 10s
	// matches the refactor.md latency contract.
	DeployTimeout time.Duration
	// PollInterval is how often Deploy re-reads the CR while waiting.
	PollInterval time.Duration

	// Quota, when set, gates NEW deploys against per-owner/per-department
	// ceilings and resolves the owner's department for labeling. nil disables
	// quota enforcement.
	Quota Quota
	// Audit, when set, records deploy/delete actions. nil disables auditing.
	Audit Auditor
	// Policy, when set, evaluates each deploy after classification and may deny
	// it. nil = no policy (deploys are unrestricted).
	Policy policy.Gate
	// Authz, when set, authorizes each per-action access (get/deploy/delete/
	// suspend) and replaces the built-in owner check. nil = built-in behavior
	// (owner-or-admin, shared read).
	Authz authz.Authorizer
	// Meter, when set, records a usage event on each deploy/delete. nil disables
	// metering.
	Meter meter.Sink
	// Metrics, when set, records Prometheus deploy/artifact counters. nil
	// disables instrumentation (e.g. in tests).
	Metrics *metrics.Metrics

	// ShareLinks persists public share links. nil disables share links (only the
	// sqlite store backend implements it). BaseURL is the public origin used to
	// build a link's shareable URL (e.g. https://apps.example.com).
	ShareLinks store.ShareLinkStore
	BaseURL    string
}

// Quota gates new deploys within a tenant and resolves the owner's department
// (for labeling). Authorize returns a non-nil release the caller must invoke
// once the new app is durably created (List-visible) or the deploy has failed;
// it drops the reservation Authorize took against the ceiling, which is what
// keeps concurrent deploys from overshooting it.
type Quota interface {
	Authorize(ctx context.Context, t tenant.Tenant, owner string, isNew bool) (department string, release func(), err error)
}

// Auditor records a mutating action; implementations read the actor from ctx.
// Returning a non-nil error means the recorder is fail-closed and the caller
// must abort the action it was about to log (preferring availability loss
// over an untraceable mutation). nil errors mean the caller can proceed.
type Auditor interface {
	Record(ctx context.Context, action, target, outcome, detail string) error
}

// Request is one deploy. Tarball is consumed fully (and capped) by Deploy.
type Request struct {
	Name    string
	Owner   string
	Tarball io.Reader

	// Overrides from the deploy metadata; empty means "let the classifier
	// decide" (lane/template) or "agent autodetect" (entrypoint).
	LaneOverride     vibedv1.Lane
	TemplateOverride string
	Entrypoint       string
	Env              []vibedv1.EnvVar
	TTL              string

	// AllowedHosts is the per-app egress allow-list (hostnames the app may
	// reach through the egress proxy). Empty = no external egress.
	AllowedHosts []string
}

// Result is what the API returns. Ready reports whether the app reached
// PhaseReady within DeployTimeout; when false the caller polls AppID.
type Result struct {
	AppID string
	Phase vibedv1.Phase
	URL   string
	Ready bool
}

func (s *Service) defaults() {
	if s.DeployTimeout == 0 {
		s.DeployTimeout = 10 * time.Second
	}
	if s.PollInterval == 0 {
		s.PollInterval = 250 * time.Millisecond
	}
	if s.Namespace == "" {
		s.Namespace = "vibed-apps"
	}
}

// Deploy classifies the source, stores the tarball, creates (or replaces)
// the VibedApp, and waits up to DeployTimeout for it to go Ready.
func (s *Service) Deploy(ctx context.Context, req Request) (*Result, error) {
	s.defaults()
	start := time.Now() // request start, observed by the DeployDuration defer below

	if !dnsName.MatchString(req.Name) {
		return nil, fmt.Errorf("invalid app name %q: must be a DNS label (lowercase alphanumeric and -)", req.Name)
	}
	if req.Owner == "" {
		return nil, fmt.Errorf("owner is required")
	}

	t, err := s.tenant(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant: %w", err)
	}

	// Read + cap the tarball once, hashing it in the same pass via a TeeReader
	// instead of walking the full (up-to-50MB) buffer a second time with
	// sha256.Sum256 — this is the latency-sensitive deploy hot path (#73). The
	// buffer is still needed in memory because the policy engine evaluates over
	// the full source bytes; the remaining passes (classify, store upload) are
	// inherent to those operations.
	hasher := sha256.New()
	buf, err := readCapped(io.TeeReader(req.Tarball, hasher), MaxTarballBytes)
	if err != nil {
		return nil, err
	}

	// Enrich the audit events for this deploy with the tenant and a hash of the
	// exact source (provenance). The context flows through every s.record call.
	sum := hasher.Sum(nil)
	ctx = audit.WithFields(ctx, audit.Fields{TenantID: t.ID, SourceHash: hex.EncodeToString(sum[:])})

	// Classify (unless both lane + template are overridden).
	lane := req.LaneOverride
	template := req.TemplateOverride
	if lane == "" || template == "" {
		dec, cerr := s.Classifier.Classify(ctx, bytes.NewReader(buf))
		if cerr != nil {
			return nil, fmt.Errorf("classify source: %w", cerr)
		}
		if lane == "" {
			lane = dec.Lane
		}
		if template == "" {
			template = dec.Template
		}
	}

	// New vs redeploy: a redeploy reuses the CR (so the controller can
	// snapshot-restore later) and is never quota-gated.
	key := types.NamespacedName{Name: req.Name, Namespace: t.Namespace}
	existing := &vibedv1.VibedApp{}
	getErr := s.Client.Get(ctx, key, existing)
	isNew := apierrors.IsNotFound(getErr)
	if getErr != nil && !isNew {
		return nil, fmt.Errorf("get VibedApp: %w", getErr)
	}

	// Deploy outcome metrics: template + isNew are known now, so a single defer
	// records success/failure for every return below (a waitReady timeout is
	// still a successful deploy — the CR was created — hence the explicit flag
	// rather than keying off the returned error). DeployDuration observes
	// request-start → return under the same status/template labels, making the
	// sub-2s p50 deploy contract measurable on this path; the vec's labels
	// carry no ready-vs-pending outcome, so a 202-pending return records as a
	// (long) success. ArtifactsActive tracks live apps, so it only rises on a
	// genuinely new app (a redeploy reuses the CR).
	deployed := false
	defer func() {
		if s.Metrics == nil {
			return
		}
		status := "success"
		if !deployed {
			status = "failed"
		}
		s.Metrics.DeploysTotal.WithLabelValues(status, template).Inc()
		s.Metrics.DeployDuration.WithLabelValues(status, template).Observe(time.Since(start).Seconds())
		if deployed && isNew {
			s.Metrics.ArtifactsActive.WithLabelValues(template).Inc()
		}
	}()

	// Policy gate: evaluated on every deploy (new AND redeploy — a redeploy can
	// introduce a new violation) after classification, before anything is
	// stored. A denial aborts the deploy.
	if s.Policy != nil {
		if perr := s.Policy.Evaluate(ctx, policy.Input{
			Tenant:       t,
			Owner:        req.Owner,
			Name:         req.Name,
			Lane:         lane,
			Template:     template,
			AllowedHosts: req.AllowedHosts,
			Env:          req.Env,
			IsNew:        isNew,
			Source:       buf,
		}); perr != nil {
			_ = s.record(ctx, "deploy", req.Name, "denied", perr.Error())
			return nil, perr
		}
	}

	// Authorization: may this caller deploy into the target namespace/team?
	// Consulted on new deploys and redeploys alike.
	if s.Authz != nil {
		if aerr := s.Authz.Authorize(ctx, authz.Request{
			Subject: req.Owner,
			Role:    vibedauth.RoleFromContext(ctx),
			Action:  authz.ActionAppDeploy,
			Resource: authz.Resource{
				Kind: "app", ID: req.Name, Owner: req.Owner, Namespace: t.Namespace,
			},
		}); aerr != nil {
			_ = s.record(ctx, "deploy", req.Name, "denied", aerr.Error())
			return nil, aerr
		}
	}

	// Quota gates new deploys (within the tenant) and tells us the owner's
	// department for labeling. The reservation Authorize takes must be released
	// once the new app is durably created (List-visible via the direct client)
	// or the deploy fails — releaseQuota, called both after the CR write and via
	// defer, does that; it's idempotent, so the defer is a safety net for the
	// error paths in between.
	var department string
	releaseQuota := func() {}
	defer func() { releaseQuota() }()
	if s.Quota != nil {
		dept, release, qerr := s.Quota.Authorize(ctx, t, req.Owner, isNew)
		if qerr != nil {
			_ = s.record(ctx, "deploy", req.Name, "denied", qerr.Error())
			return nil, qerr
		}
		releaseQuota = release
		department = dept
	}

	// Version this deploy: continue the app's history on a redeploy, else start
	// at v1. Each version's source is retained under its own key so a rollback
	// can re-deploy it.
	var history []VersionRecord
	if !isNew {
		history = loadVersions(existing)
	}
	version := nextVersion(history)
	tarballKey := versionKey(req.Name, version)

	// Store the tarball so the agent can pull it.
	refURL, err := s.Store.Put(ctx, tarballKey, bytes.NewReader(buf))
	if err != nil {
		_ = s.record(ctx, "deploy", req.Name, "error", err.Error())
		return nil, fmt.Errorf("store source: %w", err)
	}

	keptVersions, evicted := appendCapped(history, VersionRecord{
		Version:    version,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Template:   template,
		Lane:       string(lane),
		SourceHash: hex.EncodeToString(sum[:]),
		TarballKey: tarballKey,
		TarballRef: refURL,
	}, maxVersions)
	versionsJSON := marshalVersions(keptVersions)

	labels := map[string]string{vibedv1.LabelOwner: vibedv1.SanitizeLabel(req.Owner)}
	if department != "" {
		labels[vibedv1.LabelDepartment] = vibedv1.SanitizeLabel(department)
	}
	if t.ID != "" {
		labels[vibedv1.LabelTenant] = vibedv1.SanitizeLabel(t.ID)
	}

	spec := vibedv1.VibedAppSpec{
		Owner:  req.Owner,
		Source: vibedv1.Source{TarballRef: refURL},
		Runtime: vibedv1.Runtime{
			Lane:       lane,
			Template:   template,
			Entrypoint: req.Entrypoint,
			Env:        req.Env,
		},
		Egress: vibedv1.Egress{AllowedHosts: req.AllowedHosts},
		TTL:    req.TTL,
	}

	if isNew {
		app := &vibedv1.VibedApp{
			ObjectMeta: metav1.ObjectMeta{
				Name:        req.Name,
				Namespace:   t.Namespace,
				Labels:      labels,
				Annotations: map[string]string{versionsAnnotation: versionsJSON},
			},
			Spec: spec,
		}
		if cerr := s.Client.Create(ctx, app); cerr != nil {
			_ = s.Store.Delete(ctx, tarballKey) // don't leave an orphan blob
			_ = s.record(ctx, "deploy", req.Name, "error", cerr.Error())
			return nil, fmt.Errorf("create VibedApp: %w", cerr)
		}
	} else {
		existing.Spec = spec
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		for k, v := range labels {
			existing.Labels[k] = v
		}
		anns := existing.GetAnnotations()
		if anns == nil {
			anns = map[string]string{}
		}
		anns[versionsAnnotation] = versionsJSON
		existing.SetAnnotations(anns)
		if uerr := s.Client.Update(ctx, existing); uerr != nil {
			_ = s.record(ctx, "deploy", req.Name, "error", uerr.Error())
			return nil, fmt.Errorf("update VibedApp: %w", uerr)
		}
	}

	// The CR is durably written and now List-visible via the direct client, so
	// the quota reservation is redundant with the live count — drop it promptly
	// (rather than only on the deferred path after waitReady) so it isn't
	// double-counted against a concurrent deploy at the exact ceiling boundary.
	releaseQuota()

	// Prune source tarballs for versions that aged out of the retained history.
	for _, k := range evicted {
		_ = s.Store.Delete(ctx, k)
	}

	// Success-path audit: when the recorder is fail-closed and the audit
	// write fails, surface the error to the API caller. The VibedApp is
	// already created/updated and the controller will reconcile it, but
	// the API tells the caller "we couldn't durably record this" so they
	// know to retry (deploy is idempotent) or alert the operator.
	if err := s.record(ctx, "deploy", req.Name, "ok", ""); err != nil {
		return nil, fmt.Errorf("deploy succeeded but audit failed: %w", err)
	}
	s.meter(ctx, meter.Event{Kind: "deploy", Tenant: t.ID, Owner: req.Owner, App: req.Name, Namespace: t.Namespace})
	deployed = true
	return s.waitReady(ctx, t.Namespace, req.Name)
}

// meter records a usage event when a Meter is configured (else no-op).
func (s *Service) meter(ctx context.Context, e meter.Event) {
	if s.Meter != nil {
		s.Meter.Record(ctx, e)
	}
}

// tenant resolves the request's tenant, falling back to the single-tenant
// default (Service.Namespace, no limits) when no resolver is configured or the
// resolved tenant leaves the namespace empty.
func (s *Service) tenant(ctx context.Context) (tenant.Tenant, error) {
	var t tenant.Tenant
	if s.Tenants != nil {
		var err error
		if t, err = s.Tenants.Resolve(ctx); err != nil {
			return t, err
		}
	}
	// A named tenant must carry its own namespace; falling back to the shared
	// default would silently mix it with other tenants. Only the single-tenant
	// default (empty ID) inherits Service.Namespace.
	if t.ID != "" && t.Namespace == "" {
		return t, fmt.Errorf("tenant %q resolved without a namespace", t.ID)
	}
	if t.Namespace == "" {
		t.Namespace = s.Namespace
	}
	return t, nil
}

// record emits an audit event when an Auditor is configured (else no-op).
// Returns the recorder's error verbatim so the caller can decide whether to
// abort (used on the success path; pre-action paths intentionally drop the
// error because they're already returning a failure).
func (s *Service) record(ctx context.Context, action, target, outcome, detail string) error {
	if s.Audit == nil {
		return nil
	}
	return s.Audit.Record(ctx, action, target, outcome, detail)
}

// waitReady polls the CR until it reaches Ready/Failed or DeployTimeout
// elapses. A timeout is not an error — it's the 202 (still-claiming) path.
func (s *Service) waitReady(ctx context.Context, namespace, name string) (*Result, error) {
	deadline := time.Now().Add(s.DeployTimeout)
	key := types.NamespacedName{Name: name, Namespace: namespace}

	for {
		app := &vibedv1.VibedApp{}
		if err := s.Client.Get(ctx, key, app); err != nil {
			return nil, fmt.Errorf("get VibedApp while waiting: %w", err)
		}
		switch app.Status.Phase {
		case vibedv1.PhaseReady:
			return &Result{AppID: name, Phase: app.Status.Phase, URL: app.Status.URL, Ready: true}, nil
		case vibedv1.PhaseFailed:
			return &Result{AppID: name, Phase: app.Status.Phase, URL: app.Status.URL, Ready: false}, nil
		}
		if time.Now().After(deadline) {
			return &Result{AppID: name, Phase: app.Status.Phase, Ready: false}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.PollInterval):
		}
	}
}

func readCapped(r io.Reader, max int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	if int64(len(buf)) > max {
		return nil, fmt.Errorf("source exceeds %d MB limit", max/(1024*1024))
	}
	if len(buf) == 0 {
		return nil, fmt.Errorf("source is empty")
	}
	return buf, nil
}
