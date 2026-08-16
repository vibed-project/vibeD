package deploy

import (
	"context"
	"fmt"
	"log/slog"

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

// appRequest builds the authz.Request for action on app by owner. app may be
// nil for collection/creation actions, in which case only the identity is
// carried. Both the per-item and batch authorization paths build Requests
// here, so they decide from identical inputs.
func (s *Service) appRequest(ctx context.Context, action authz.Action, app *vibedv1.VibedApp, owner string) authz.Request {
	res := authz.Resource{Kind: "app"}
	if app != nil {
		res.ID = app.Name
		res.Owner = app.Spec.Owner
		res.Namespace = app.Namespace
		res.Department = app.Labels[vibedv1.LabelDepartment]
	}
	return authz.Request{
		Subject:  owner,
		Role:     vibedauth.RoleFromContext(ctx),
		Action:   action,
		Resource: res,
	}
}

// authorize consults the registered Authorizer for action on app by owner. It
// returns nil (allow) when no Authorizer is registered.
func (s *Service) authorize(ctx context.Context, action authz.Action, app *vibedv1.VibedApp, owner string) error {
	if s.Authz == nil {
		return nil
	}
	return s.Authz.Authorize(ctx, s.appRequest(ctx, action, app, owner))
}

// filterAuthorized returns the subset of apps the caller may read, in input
// order. When the Authorizer also implements authz.BatchAuthorizer, all
// candidates are decided in one AuthorizeBatch call; otherwise — or when the
// batch reply is not length-matched to the requests and so cannot be trusted —
// it falls back to per-item Authorize, the semantics of record. Both paths
// produce identical decisions by the BatchAuthorizer contract.
func (s *Service) filterAuthorized(ctx context.Context, apps []vibedv1.VibedApp, owner string) []vibedv1.VibedApp {
	out := make([]vibedv1.VibedApp, 0, len(apps))
	if ba, ok := s.Authz.(authz.BatchAuthorizer); ok {
		reqs := make([]authz.Request, len(apps))
		for i := range apps {
			reqs[i] = s.appRequest(ctx, authz.ActionAppGet, &apps[i], owner)
		}
		if errs := ba.AuthorizeBatch(ctx, reqs); len(errs) == len(reqs) {
			for i := range apps {
				if errs[i] == nil {
					out = append(out, apps[i])
				}
			}
			return out
		}
		// Length mismatch: alignment is unknowable, so ignore the batch reply
		// and decide per item below.
	}
	for i := range apps {
		if s.authorize(ctx, authz.ActionAppGet, &apps[i], owner) == nil {
			out = append(out, apps[i])
		}
	}
	return out
}

// List returns the VibedApps owned by owner (or visible to them via the
// Authorizer) in the request's tenant namespace, plus the total count of
// visible apps. Pagination is applied AFTER ownership/authorization
// filtering — matching the sqlite store's ListOptions semantics — so
// limit/offset slice the caller-visible set, never the raw namespace. limit
// <= 0 returns everything after offset; a negative offset is clamped to 0.
func (s *Service) List(ctx context.Context, owner string, limit, offset int) ([]vibedv1.VibedApp, int, error) {
	s.defaults()
	t, err := s.tenant(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve tenant: %w", err)
	}
	listOpts := []client.ListOption{client.InNamespace(t.Namespace)}
	// Without an Authorizer, List is owner-only, so pre-filter server-side by the
	// owner label (#74) instead of fetching the whole namespace. The label is a
	// lossy mirror of spec.owner (collisions possible — #66), so it can only
	// over-return; the exact spec.owner check below stays authoritative. With an
	// Authorizer we must by default fetch every app in the namespace so it can
	// grant team-scoped visibility (apps the caller doesn't own) — unless the
	// Authorizer also implements authz.ListScoper, whose selector narrows the
	// fetch. That selector may likewise only over-return (candidates are still
	// exactly filtered below), so applying it cannot change the result, only
	// shrink the fetch; a nil selector declines narrowing.
	if s.Authz == nil {
		listOpts = append(listOpts, client.MatchingLabels{vibedv1.LabelOwner: vibedv1.SanitizeLabel(owner)})
	} else if sc, ok := s.Authz.(authz.ListScoper); ok {
		req := s.appRequest(ctx, authz.ActionAppList, nil, owner)
		req.Resource.Namespace = t.Namespace
		sel, serr := sc.ScopeList(ctx, req)
		if serr != nil {
			return nil, 0, fmt.Errorf("scope list: %w", serr)
		}
		if sel != nil {
			listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: sel})
		}
	}
	var list vibedv1.VibedAppList
	if err := s.Client.List(ctx, &list, listOpts...); err != nil {
		return nil, 0, fmt.Errorf("list VibedApps: %w", err)
	}
	// With an Authorizer, list every fetched app the caller may read (team-
	// scoped visibility, e.g. a Viewer seeing team apps), batching the decisions
	// when supported. Without one, keep the built-in owner-only scoping.
	var out []vibedv1.VibedApp
	if s.Authz != nil {
		out = s.filterAuthorized(ctx, list.Items, owner)
	} else {
		out = make([]vibedv1.VibedApp, 0, len(list.Items))
		for i := range list.Items {
			if list.Items[i].Spec.Owner == owner {
				out = append(out, list.Items[i])
			}
		}
	}
	total := len(out)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
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
	// Revoke this app's share links. ResolveShareLink looks the app up by NAME
	// at request time, so a link left live after deletion silently re-binds to
	// whatever app next takes that name — publishing a different (possibly
	// another user's) app to everyone holding the old URL. Best-effort: the CR
	// is already gone, and a failure here must not fail the delete, but it is
	// logged rather than swallowed so a stuck revoke is visible.
	if s.ShareLinks != nil {
		links, lerr := s.ShareLinks.ListShareLinks(ctx, id, 0, 0)
		if lerr != nil {
			slog.Default().Warn("could not list share links to revoke on delete", "app", id, "error", lerr)
		}
		for _, l := range links {
			if l.Revoked {
				continue
			}
			if rerr := s.ShareLinks.RevokeShareLink(ctx, l.Token); rerr != nil {
				slog.Default().Warn("could not revoke share link on delete", "app", id, "error", rerr)
			}
		}
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
