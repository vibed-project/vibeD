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
	"hash"
	"io"
	"os"
	"regexp"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
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

	// Watcher, when set, lets waitReady watch the VibedApp for its Ready/Failed
	// transition instead of re-Getting it every PollInterval — one API call plus
	// a watch stream instead of up to DeployTimeout/PollInterval round-trips on
	// the latency-sensitive deploy path. Optional: when nil (or when the watch
	// fails to start) waitReady falls back to the poll loop, so constructors
	// passing only a plain Client keep working unchanged.
	Watcher client.WithWatch

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

	// Reason and Message carry the Ready condition's explanation for a
	// terminal phase. On a failure that never produced a pod (e.g. no warm
	// pool for the required template) this is the ONLY diagnosis available —
	// there are no logs to fetch — so callers must surface it rather than
	// reporting a bare "failed".
	Reason  string
	Message string
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

	// Acquire the source. With a policy gate configured the full tarball is
	// still buffered in memory, because policy.Input.Source is a []byte
	// contract gates evaluate over. Without one — the default; policy.Build
	// returns nil unless an out-of-tree gate registered — the source is
	// streamed instead: one hashing pass over the request body, then classify
	// and Store.Put re-read the same bytes by rewinding the caller's reader
	// when it is seekable (the multipart layer hands us a seekable file/section
	// either way) or a local spool file written during the hashing pass. The
	// nil-gate path therefore never allocates a full-payload buffer; peak heap
	// stays at io.Copy's fixed buffer no matter the tarball size. Both paths
	// hash in-pass via a TeeReader-style tee (#73), enforce MaxTarballBytes
	// with identical errors, and produce identical sha256 bytes.
	hasher := sha256.New()
	var (
		buf        []byte // buffered path only (s.Policy != nil)
		openSource func() (io.Reader, error)
	)
	if s.Policy != nil {
		b, rerr := readCapped(io.TeeReader(req.Tarball, hasher), MaxTarballBytes)
		if rerr != nil {
			return nil, rerr
		}
		buf = b
		openSource = func() (io.Reader, error) { return bytes.NewReader(buf), nil }
	} else {
		open, cleanup, serr := streamSource(req.Tarball, hasher, MaxTarballBytes)
		if serr != nil {
			return nil, serr
		}
		defer cleanup()
		openSource = open
	}

	// Enrich the audit events for this deploy with the tenant and a hash of the
	// exact source (provenance). The context flows through every s.record call.
	sum := hasher.Sum(nil)
	ctx = audit.WithFields(ctx, audit.Fields{TenantID: t.ID, SourceHash: hex.EncodeToString(sum[:])})

	// Classify (unless both lane + template are overridden).
	lane := req.LaneOverride
	template := req.TemplateOverride
	if lane == "" || template == "" {
		src, oerr := openSource()
		if oerr != nil {
			return nil, oerr
		}
		dec, cerr := s.Classifier.Classify(ctx, src)
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
			// SourceOpener lets gates stream the source instead of holding
			// Source; here it opens over the same buffer, so migrating gates
			// see identical bytes either way.
			SourceOpener: func(context.Context) (io.ReadCloser, error) {
				src, oerr := openSource()
				if oerr != nil {
					return nil, oerr
				}
				return io.NopCloser(src), nil
			},
		}); perr != nil {
			_ = s.record(ctx, "deploy", req.Name, "denied", perr.Error())
			return nil, perr
		}
	}

	// Ownership gate for a redeploy. A redeploy mutates an EXISTING app, so the
	// caller must own it (or be an admin). Without this, deploying under a name
	// someone else already used silently takes over their app — replacing its
	// source and serving the attacker's code on the victim's URL — because
	// nothing downstream compares owners. Apps with no recorded owner (legacy
	// rows) stay open so this can't strand them.
	callerRole := vibedauth.RoleFromContext(ctx)
	if !isNew && existing.Spec.Owner != "" && existing.Spec.Owner != req.Owner && callerRole != "admin" {
		aerr := &authz.DeniedError{
			Action: authz.ActionAppDeploy,
			Reason: "app is owned by another user",
		}
		_ = s.record(ctx, "deploy", req.Name, "denied", aerr.Error())
		return nil, aerr
	}

	// Authorization: may this caller deploy into the target namespace/team?
	// Consulted on new deploys and redeploys alike. Resource.Owner must be the
	// EXISTING app's owner on a redeploy — passing the caller's own id would
	// describe every request as self-owned and blind any ownership-aware
	// authorizer (e.g. the enterprise RBAC one) to a takeover attempt.
	resourceOwner := req.Owner
	if !isNew && existing.Spec.Owner != "" {
		resourceOwner = existing.Spec.Owner
	}
	if s.Authz != nil {
		if aerr := s.Authz.Authorize(ctx, authz.Request{
			Subject: req.Owner,
			Role:    callerRole,
			Action:  authz.ActionAppDeploy,
			Resource: authz.Resource{
				Kind: "app", ID: req.Name, Owner: resourceOwner, Namespace: t.Namespace,
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
	tarballKey, kerr := newVersionKey(req.Name, version)
	if kerr != nil {
		_ = s.record(ctx, "deploy", req.Name, "error", kerr.Error())
		return nil, kerr
	}

	// Store the tarball so the agent can pull it. On the streaming path this
	// re-reads the caller's seekable reader or the spool file; the spool is an
	// *os.File, which the served store can hard-link instead of re-copying.
	src, oerr := openSource()
	if oerr != nil {
		_ = s.record(ctx, "deploy", req.Name, "error", oerr.Error())
		return nil, fmt.Errorf("store source: %w", oerr)
	}
	refURL, err := s.Store.Put(ctx, tarballKey, src)
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

// waitReady waits for the CR to reach Ready/Failed or for DeployTimeout to
// elapse. A timeout is not an error — it's the 202 (still-claiming) path.
// With a Watcher it consumes a server-side watch (one Get + an event stream);
// without one — or when the watch can't be established — it falls back to
// re-Getting the CR every PollInterval.
func (s *Service) waitReady(ctx context.Context, namespace, name string) (*Result, error) {
	deadline := time.Now().Add(s.DeployTimeout)
	key := types.NamespacedName{Name: name, Namespace: namespace}

	if s.Watcher != nil {
		w, err := s.Watcher.Watch(ctx, &vibedv1.VibedAppList{},
			client.InNamespace(namespace),
			client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("metadata.name", name)},
		)
		if err == nil {
			return s.watchReady(ctx, w, key, deadline)
		}
		// Watch setup failed (e.g. RBAC without watch verb) — poll instead.
	}
	return s.pollReady(ctx, key, deadline)
}

// watchReady consumes watch events until the app goes Ready/Failed or the
// deadline passes. The watch starts before the initial Get so no transition
// can fall between them: a plain watch does not replay existing objects, so
// the Get catches an app that was already Ready/Failed, and anything that
// changes afterwards arrives as an event.
func (s *Service) watchReady(ctx context.Context, w watch.Interface, key types.NamespacedName, deadline time.Time) (*Result, error) {
	defer w.Stop()
	name := key.Name

	app := &vibedv1.VibedApp{}
	if err := s.Client.Get(ctx, key, app); err != nil {
		return nil, fmt.Errorf("get VibedApp while waiting: %w", err)
	}
	if res := phaseResult(name, app); res != nil {
		return res, nil
	}
	lastPhase := app.Status.Phase

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return &Result{AppID: name, Phase: lastPhase, Ready: false}, nil
		case ev, ok := <-w.ResultChan():
			if !ok {
				// Server closed the watch early — finish out on the poll loop.
				return s.pollReady(ctx, key, deadline)
			}
			evApp, ok := ev.Object.(*vibedv1.VibedApp)
			if !ok {
				continue // watch.Error carries *metav1.Status; Bookmark carries no app
			}
			if evApp.Name != name || evApp.Namespace != key.Namespace {
				continue // defense in depth; the field selector filters server-side
			}
			if ev.Type == watch.Deleted {
				return nil, fmt.Errorf("get VibedApp while waiting: %w",
					apierrors.NewNotFound(vibedv1.GroupVersion.WithResource("vibedapps").GroupResource(), name))
			}
			lastPhase = evApp.Status.Phase
			if res := phaseResult(name, evApp); res != nil {
				return res, nil
			}
		}
	}
}

// pollReady is the watchless fallback: re-Get the CR every PollInterval until
// it reaches Ready/Failed or the deadline passes.
func (s *Service) pollReady(ctx context.Context, key types.NamespacedName, deadline time.Time) (*Result, error) {
	name := key.Name
	for {
		app := &vibedv1.VibedApp{}
		if err := s.Client.Get(ctx, key, app); err != nil {
			return nil, fmt.Errorf("get VibedApp while waiting: %w", err)
		}
		if res := phaseResult(name, app); res != nil {
			return res, nil
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

// phaseResult maps a terminal phase to its Result; nil means keep waiting.
func phaseResult(name string, app *vibedv1.VibedApp) *Result {
	switch app.Status.Phase {
	case vibedv1.PhaseReady:
		return withReadyCondition(&Result{AppID: name, Phase: app.Status.Phase, URL: app.Status.URL, Ready: true}, app)
	case vibedv1.PhaseFailed:
		return withReadyCondition(&Result{AppID: name, Phase: app.Status.Phase, URL: app.Status.URL, Ready: false}, app)
	}
	return nil
}

// withReadyCondition copies the Ready condition's reason/message onto the
// Result so failures explain themselves to every caller (HTTP, MCP, CLI).
func withReadyCondition(res *Result, app *vibedv1.VibedApp) *Result {
	if c := meta.FindStatusCondition(app.Status.Conditions, vibedv1.ConditionReady); c != nil {
		res.Reason, res.Message = c.Reason, c.Message
	}
	return res
}

func readCapped(r io.Reader, max int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	if err := checkSize(int64(len(buf)), max); err != nil {
		return nil, err
	}
	return buf, nil
}

// checkSize is the post-read size contract shared by readCapped (buffered
// path) and streamSource (streaming path), so both fail with byte-identical
// errors.
func checkSize(n, max int64) error {
	if n > max {
		return fmt.Errorf("source exceeds %d MB limit", max/(1024*1024))
	}
	if n == 0 {
		return fmt.Errorf("source is empty")
	}
	return nil
}

// streamSource prepares the request tarball for the nil-policy deploy path
// without buffering the payload: it hashes the full stream into hasher in one
// pass, enforcing max exactly like readCapped, and returns an open function
// that re-reads the same bytes from the start on every call — by rewinding the
// caller's reader when it is seekable (both multipart shapes are: the >8MB
// disk spill and the in-memory section), else from a spool file written during
// the hashing pass. cleanup removes the spool (a no-op for seekable input) and
// must be called once the source is no longer needed; on error streamSource
// has already cleaned up after itself.
func streamSource(r io.Reader, hasher hash.Hash, max int64) (open func() (io.Reader, error), cleanup func(), err error) {
	if rs, ok := r.(io.ReadSeeker); ok {
		if start, serr := rs.Seek(0, io.SeekCurrent); serr == nil {
			n, cerr := io.Copy(hasher, io.LimitReader(rs, max+1))
			if cerr != nil {
				return nil, nil, fmt.Errorf("read source: %w", cerr)
			}
			if err := checkSize(n, max); err != nil {
				return nil, nil, err
			}
			open = func() (io.Reader, error) {
				if _, serr := rs.Seek(start, io.SeekStart); serr != nil {
					return nil, fmt.Errorf("rewind source: %w", serr)
				}
				return rs, nil
			}
			return open, func() {}, nil
		}
		// A ReadSeeker whose Seek fails (e.g. an *os.File over a pipe) spools
		// like any non-seekable reader.
	}

	tmp, terr := os.CreateTemp("", "vibed-deploy-*.tar.gz")
	if terr != nil {
		return nil, nil, fmt.Errorf("spool source: %w", terr)
	}
	cleanup = func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	src := &taggedReader{r: io.LimitReader(r, max+1)}
	n, cerr := io.Copy(tmp, io.TeeReader(src, hasher))
	if cerr != nil {
		cleanup()
		if cerr == src.err { // the request body failed, not the spool write
			return nil, nil, fmt.Errorf("read source: %w", cerr)
		}
		return nil, nil, fmt.Errorf("spool source: %w", cerr)
	}
	if err := checkSize(n, max); err != nil {
		cleanup()
		return nil, nil, err
	}
	open = func() (io.Reader, error) {
		if _, serr := tmp.Seek(0, io.SeekStart); serr != nil {
			return nil, fmt.Errorf("rewind source: %w", serr)
		}
		return tmp, nil
	}
	return open, cleanup, nil
}

// taggedReader remembers the error its inner reader returned, so a failed
// io.Copy through it can be attributed to the read side vs the write side.
type taggedReader struct {
	r   io.Reader
	err error
}

func (t *taggedReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if err != nil && err != io.EOF {
		t.err = err
	}
	return n, err
}
