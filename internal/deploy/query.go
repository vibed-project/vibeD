package deploy

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// ErrNotFound is returned by Get/Delete when no app matches (or the caller
// doesn't own it — we deliberately don't distinguish, to avoid leaking the
// existence of other users' apps).
var ErrNotFound = fmt.Errorf("app not found")

// Get returns the VibedApp named id if owner owns it, else ErrNotFound.
func (s *Service) Get(ctx context.Context, owner, id string) (*vibedv1.VibedApp, error) {
	s.defaults()
	app := &vibedv1.VibedApp{}
	err := s.Client.Get(ctx, types.NamespacedName{Name: id, Namespace: s.Namespace}, app)
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

// List returns every VibedApp owned by owner in the service namespace.
func (s *Service) List(ctx context.Context, owner string) ([]vibedv1.VibedApp, error) {
	s.defaults()
	var list vibedv1.VibedAppList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
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
		s.record(ctx, action, id, "error", uerr.Error())
		return nil, fmt.Errorf("update VibedApp: %w", uerr)
	}
	s.record(ctx, action, id, "ok", "")
	return app, nil
}

// Delete removes the VibedApp (and its tarball) if owner owns it. The
// controller's ownerRef on the SandboxClaim reaps the bound pod.
func (s *Service) Delete(ctx context.Context, owner, id string) error {
	app, err := s.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	if derr := s.Client.Delete(ctx, app); derr != nil && !apierrors.IsNotFound(derr) {
		s.record(ctx, "delete", id, "error", derr.Error())
		return fmt.Errorf("delete VibedApp: %w", derr)
	}
	// Best-effort tarball cleanup; the CR is already gone either way.
	_ = s.Store.Delete(ctx, id)
	s.record(ctx, "delete", id, "ok", "")
	return nil
}
