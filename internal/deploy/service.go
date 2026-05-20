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

	// Namespace is where VibedApp CRs are created.
	Namespace string
	// DeployTimeout bounds how long Deploy waits for Ready before returning
	// a still-pending result (the API turns this into a 202). Default 10s
	// matches the refactor.md latency contract.
	DeployTimeout time.Duration
	// PollInterval is how often Deploy re-reads the CR while waiting.
	PollInterval time.Duration
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

	// Store the tarball so the agent can pull it.
	refURL, err := s.Store.Put(ctx, req.Name, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("store source: %w", err)
	}

	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: s.Namespace,
			Labels:    map[string]string{"vibed.dev/owner": sanitizeLabel(req.Owner)},
		},
		Spec: vibedv1.VibedAppSpec{
			Owner:  req.Owner,
			Source: vibedv1.Source{TarballRef: refURL},
			Runtime: vibedv1.Runtime{
				Lane:       lane,
				Template:   template,
				Entrypoint: req.Entrypoint,
				Env:        req.Env,
			},
			TTL: req.TTL,
		},
	}

	if err := s.upsert(ctx, app); err != nil {
		// Best-effort: don't leave an orphan blob if the CR write failed.
		_ = s.Store.Delete(ctx, req.Name)
		return nil, err
	}

	return s.waitReady(ctx, req.Name)
}

// upsert creates the VibedApp, or updates the spec of an existing one (a
// redeploy under the same name reuses the CR so the controller can
// snapshot-restore later).
func (s *Service) upsert(ctx context.Context, app *vibedv1.VibedApp) error {
	existing := &vibedv1.VibedApp{}
	key := types.NamespacedName{Name: app.Name, Namespace: app.Namespace}
	err := s.Client.Get(ctx, key, existing)
	switch {
	case apierrors.IsNotFound(err):
		if cerr := s.Client.Create(ctx, app); cerr != nil {
			return fmt.Errorf("create VibedApp: %w", cerr)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get VibedApp: %w", err)
	default:
		existing.Spec = app.Spec
		existing.Labels = app.Labels
		if uerr := s.Client.Update(ctx, existing); uerr != nil {
			return fmt.Errorf("update VibedApp: %w", uerr)
		}
		return nil
	}
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

// sanitizeLabel makes an owner identity safe as a label value (≤63 chars,
// alnum/-/_/. only). Used only for filtering; the authoritative owner is
// spec.owner.
func sanitizeLabel(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 63 {
		out = out[:63]
	}
	return string(out)
}
