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
	"fmt"
	"io"
	"regexp"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/classifier"
	"github.com/vibed-project/vibeD/internal/tarball"
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

	// Namespace is where VibedApp CRs are created.
	Namespace string
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
}

// Quota gates new deploys and resolves the owner's department (for labeling).
type Quota interface {
	Authorize(ctx context.Context, owner string, isNew bool) (department string, err error)
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

	if !dnsName.MatchString(req.Name) {
		return nil, fmt.Errorf("invalid app name %q: must be a DNS label (lowercase alphanumeric and -)", req.Name)
	}
	if req.Owner == "" {
		return nil, fmt.Errorf("owner is required")
	}

	// Read + cap the tarball once; we feed the same bytes to the classifier
	// and the store.
	buf, err := readCapped(req.Tarball, MaxTarballBytes)
	if err != nil {
		return nil, err
	}

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
	key := types.NamespacedName{Name: req.Name, Namespace: s.Namespace}
	existing := &vibedv1.VibedApp{}
	getErr := s.Client.Get(ctx, key, existing)
	isNew := apierrors.IsNotFound(getErr)
	if getErr != nil && !isNew {
		return nil, fmt.Errorf("get VibedApp: %w", getErr)
	}

	// Quota gates new deploys and tells us the owner's department for labeling.
	var department string
	if s.Quota != nil {
		dept, qerr := s.Quota.Authorize(ctx, req.Owner, isNew)
		if qerr != nil {
			_ = s.record(ctx, "deploy", req.Name, "denied", qerr.Error())
			return nil, qerr
		}
		department = dept
	}

	// Store the tarball so the agent can pull it.
	refURL, err := s.Store.Put(ctx, req.Name, bytes.NewReader(buf))
	if err != nil {
		_ = s.record(ctx, "deploy", req.Name, "error", err.Error())
		return nil, fmt.Errorf("store source: %w", err)
	}

	labels := map[string]string{vibedv1.LabelOwner: vibedv1.SanitizeLabel(req.Owner)}
	if department != "" {
		labels[vibedv1.LabelDepartment] = vibedv1.SanitizeLabel(department)
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
			ObjectMeta: metav1.ObjectMeta{Name: req.Name, Namespace: s.Namespace, Labels: labels},
			Spec:       spec,
		}
		if cerr := s.Client.Create(ctx, app); cerr != nil {
			_ = s.Store.Delete(ctx, req.Name) // don't leave an orphan blob
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
		if uerr := s.Client.Update(ctx, existing); uerr != nil {
			_ = s.record(ctx, "deploy", req.Name, "error", uerr.Error())
			return nil, fmt.Errorf("update VibedApp: %w", uerr)
		}
	}

	// Success-path audit: when the recorder is fail-closed and the audit
	// write fails, surface the error to the API caller. The VibedApp is
	// already created/updated and the controller will reconcile it, but
	// the API tells the caller "we couldn't durably record this" so they
	// know to retry (deploy is idempotent) or alert the operator.
	if err := s.record(ctx, "deploy", req.Name, "ok", ""); err != nil {
		return nil, fmt.Errorf("deploy succeeded but audit failed: %w", err)
	}
	return s.waitReady(ctx, req.Name)
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
func (s *Service) waitReady(ctx context.Context, name string) (*Result, error) {
	deadline := time.Now().Add(s.DeployTimeout)
	key := types.NamespacedName{Name: name, Namespace: s.Namespace}

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
