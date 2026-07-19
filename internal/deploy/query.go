package deploy

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/audit"
	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/authz"
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
	// An Authorizer replaces the built-in owner check: it may grant a teammate
	// read access. A denied read is reported as ErrNotFound (not 403) to avoid
	// leaking the existence of apps the caller cannot see, matching the built-in.
	if s.Authz != nil {
		if aerr := s.authorize(ctx, authz.ActionAppGet, app, owner); aerr != nil {
			return nil, ErrNotFound
		}
		return app, nil
	}
	if app.Spec.Owner != owner {
		return nil, ErrNotFound
	}
	return app, nil
}

// authorize consults the registered Authorizer for action on app by owner. It
// returns nil (allow) when no Authorizer is registered. app may be nil for
// collection/creation actions, in which case only the identity is checked.
func (s *Service) authorize(ctx context.Context, action authz.Action, app *vibedv1.VibedApp, owner string) error {
	if s.Authz == nil {
		return nil
	}
	res := authz.Resource{Kind: "app"}
	if app != nil {
		res.ID = app.Name
		res.Owner = app.Spec.Owner
		res.Namespace = app.Namespace
		res.Department = app.Labels[vibedv1.LabelDepartment]
	}
	return s.Authz.Authorize(ctx, authz.Request{
		Subject:  owner,
		Role:     vibedauth.RoleFromContext(ctx),
		Action:   action,
		Resource: res,
	})
}

// List returns every VibedApp owned by owner in the request's tenant namespace.
func (s *Service) List(ctx context.Context, owner string) ([]vibedv1.VibedApp, error) {
	s.defaults()
	t, err := s.tenant(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant: %w", err)
	}
	// Pre-filter server-side by the owner label so the API server returns only
	// this owner's apps instead of every app in the namespace. The label is a
	// sanitized, lossy mirror of spec.owner (distinct owners can collide onto one
	// label value — see #66), so it can only over-return, never drop a match; the
	// exact spec.owner check below is still authoritative.
	var list vibedv1.VibedAppList
	if err := s.Client.List(ctx, &list,
		client.InNamespace(t.Namespace),
		client.MatchingLabels{vibedv1.LabelOwner: vibedv1.SanitizeLabel(owner)},
	); err != nil {
		return nil, fmt.Errorf("list VibedApps: %w", err)
	}
	out := make([]vibedv1.VibedApp, 0, len(list.Items))
	for i := range list.Items {
		a := list.Items[i]
		// With an Authorizer, list every app in the tenant namespace the caller
		// may read (team-scoped visibility, e.g. a Viewer seeing team apps).
		// Without one, keep the built-in owner-only scoping.
		if s.Authz != nil {
			if s.authorize(ctx, authz.ActionAppGet, &a, owner) == nil {
				out = append(out, a)
			}
			continue
		}
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
	app, err := s.Get(ctx, owner, id) // ownership-checked (read)
	if err != nil {
		return nil, err
	}
	if aerr := s.authorize(ctx, authz.ActionAppSuspend, app, owner); aerr != nil {
		_ = s.record(ctx, action, id, "denied", aerr.Error())
		return nil, aerr
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
	if aerr := s.authorize(ctx, authz.ActionAppDelete, app, owner); aerr != nil {
		_ = s.record(ctx, "delete", id, "denied", aerr.Error())
		return aerr
	}
	if derr := s.Client.Delete(ctx, app); derr != nil && !apierrors.IsNotFound(derr) {
		_ = s.record(ctx, "delete", id, "error", derr.Error())
		return fmt.Errorf("delete VibedApp: %w", derr)
	}
	// Best-effort source cleanup; the CR is already gone either way. Remove
	// every retained version's tarball, plus the legacy per-name key for apps
	// created before versioned storage.
	for _, r := range loadVersions(app) {
		if r.TarballKey != "" {
			_ = s.Store.Delete(ctx, r.TarballKey)
		}
	}
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
