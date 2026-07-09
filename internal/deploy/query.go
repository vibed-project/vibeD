package deploy

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/audit"
	"github.com/vibed-project/vibeD/internal/meter"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// ErrNotFound is returned by Get/Delete when no app matches (or the caller
// doesn't own it — we deliberately don't distinguish, to avoid leaking the
// existence of other users' apps).
var ErrNotFound = fmt.Errorf("app not found")

// Get returns the VibedApp named id if owner owns it, else ErrNotFound. The
// lookup is scoped to the request's tenant namespace, so ownership can never
// cross a tenant boundary.
func (s *Service) Get(ctx context.Context, owner, id string) (*vibedv1.VibedApp, error) {
	s.defaults()
	t, err := s.tenant(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant: %w", err)
	}
	app := &vibedv1.VibedApp{}
	err = s.Client.Get(ctx, types.NamespacedName{Name: id, Namespace: t.Namespace}, app)
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get VibedApp: %w", err)
	}
	if app.Spec.Owner != owner {
		return nil, ErrNotFound
	}
	return app, nil
}

// List returns every VibedApp owned by owner in the request's tenant namespace.
func (s *Service) List(ctx context.Context, owner string) ([]vibedv1.VibedApp, error) {
	s.defaults()
	t, err := s.tenant(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant: %w", err)
	}
	var list vibedv1.VibedAppList
	if err := s.Client.List(ctx, &list, client.InNamespace(t.Namespace)); err != nil {
		return nil, fmt.Errorf("list VibedApps: %w", err)
	}
	out := make([]vibedv1.VibedApp, 0, len(list.Items))
	for _, a := range list.Items {
		if a.Spec.Owner == owner {
			out = append(out, a)
		}
	}
	return out, nil
}

// SetSuspended toggles spec.suspended on an app the owner owns (suspend when
// true, resume when false) and returns the updated app. The controller acts on
// the change: releasing the warm-pool pod on suspend, re-claiming on resume.
// Audited as "suspend"/"resume".
func (s *Service) SetSuspended(ctx context.Context, owner, id string, suspended bool) (*vibedv1.VibedApp, error) {
	action := "resume"
	if suspended {
		action = "suspend"
	}
	app, err := s.Get(ctx, owner, id) // ownership-checked
	if err != nil {
		return nil, err
	}
	if app.Spec.Suspended == suspended {
		return app, nil // already in the desired state
	}
	app.Spec.Suspended = suspended
	if uerr := s.Client.Update(ctx, app); uerr != nil {
		_ = s.record(ctx, action, id, "error", uerr.Error())
		return nil, fmt.Errorf("update VibedApp: %w", uerr)
	}
	if err := s.record(ctx, action, id, "ok", ""); err != nil {
		return nil, fmt.Errorf("%s succeeded but audit failed: %w", action, err)
	}
	return app, nil
}

// Delete removes the VibedApp (and its tarball) if owner owns it. The
// controller's ownerRef on the SandboxClaim reaps the bound pod.
func (s *Service) Delete(ctx context.Context, owner, id string) error {
	// Resolve the tenant once and enrich the audit events + reuse it for the
	// usage event below.
	t, _ := s.tenant(ctx)
	ctx = audit.WithFields(ctx, audit.Fields{TenantID: t.ID})

	app, err := s.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	if derr := s.Client.Delete(ctx, app); derr != nil && !apierrors.IsNotFound(derr) {
		_ = s.record(ctx, "delete", id, "error", derr.Error())
		return fmt.Errorf("delete VibedApp: %w", derr)
	}
	// Best-effort tarball cleanup; the CR is already gone either way.
	_ = s.Store.Delete(ctx, id)
	if err := s.record(ctx, "delete", id, "ok", ""); err != nil {
		return fmt.Errorf("delete succeeded but audit failed: %w", err)
	}
	if s.Metrics != nil {
		// Mirror the Deploy-path Inc so the live-apps gauge stays balanced.
		s.Metrics.ArtifactsActive.WithLabelValues(app.Spec.Runtime.Template).Dec()
	}
	// Usage event, reusing the tenant resolved above.
	s.meter(ctx, meter.Event{Kind: "delete", Tenant: t.ID, Owner: owner, App: id, Namespace: t.Namespace})
	return nil
}
