// Package quota enforces per-owner and per-department caps on how many
// concurrent apps may be deployed (enterprise governance). The Enforcer is
// consulted by the deploy path before a NEW VibedApp is created; redeploys of
// an existing app are never gated. Counts are taken over live VibedApps via
// the vibed.dev/owner and vibed.dev/department labels the deploy path stamps.
package quota

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/tenant"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// UserResolver is the slice of the user store the enforcer needs to map an
// owner to a department. store.UserStore satisfies it.
type UserResolver interface {
	GetUser(ctx context.Context, id string) (*api.User, error)
	GetDepartment(ctx context.Context, id string) (*api.Department, error)
}

// rejections counts deploys turned away by quota, by the ceiling that tripped.
var rejections = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "vibed",
	Name:      "quota_rejections_total",
	Help:      "Deploys rejected by quota, by scope (tenant|owner|department).",
}, []string{"scope"})

// ExceededError reports which ceiling a deploy would have crossed. The deploy
// HTTP layer detects it via the QuotaExceeded() method (no import needed) and
// maps it to 429.
type ExceededError struct {
	Scope      string // "tenant", "owner", or "department"
	Tenant     string
	Owner      string
	Department string
	Limit      int
	Current    int
}

func (e *ExceededError) Error() string {
	switch e.Scope {
	case "tenant":
		return fmt.Sprintf("tenant %q quota exceeded: %d/%d apps already deployed", e.Tenant, e.Current, e.Limit)
	case "department":
		return fmt.Sprintf("department %q quota exceeded: %d/%d apps already deployed", e.Department, e.Current, e.Limit)
	default:
		return fmt.Sprintf("owner %q quota exceeded: %d/%d apps already deployed", e.Owner, e.Current, e.Limit)
	}
}

// QuotaExceeded lets callers detect a quota rejection via errors.As against an
// interface, without importing this package.
func (e *ExceededError) QuotaExceeded() bool { return true }

// Enforcer counts apps and gates new deploys. users may be nil (then no
// department resolution happens and only the per-owner ceiling applies).
type Enforcer struct {
	client    client.Client
	users     UserResolver
	namespace string
	cfg       config.QuotasConfig
}

// NewEnforcer builds an Enforcer over the apps namespace.
func NewEnforcer(c client.Client, users UserResolver, namespace string, cfg config.QuotasConfig) *Enforcer {
	return &Enforcer{client: c, users: users, namespace: namespace, cfg: cfg}
}

// Authorize resolves owner's department (returned so the caller can label the
// app) and, when isNew, rejects the deploy if it would exceed the tenant's total
// ceiling or the per-owner/per-department ceiling. All counts are scoped to the
// tenant's namespace (falling back to the enforcer's default namespace when the
// tenant leaves it empty), so ceilings are enforced per tenant. Redeploys
// (isNew=false) skip the ceiling checks but still return the department.
func (e *Enforcer) Authorize(ctx context.Context, t tenant.Tenant, owner string, isNew bool) (department string, err error) {
	ns := t.Namespace
	if ns == "" {
		ns = e.namespace
	}

	department = e.departmentFor(ctx, owner)
	if !isNew {
		return department, nil
	}

	// Per-tenant total ceiling (all apps in the tenant namespace).
	if max := t.Limits.MaxApps; max > 0 {
		n, cerr := e.countAll(ctx, ns)
		if cerr != nil {
			return department, cerr
		}
		if n >= max {
			rejections.WithLabelValues("tenant").Inc()
			return department, &ExceededError{Scope: "tenant", Tenant: t.ID, Limit: max, Current: n}
		}
	}

	if max := e.cfg.MaxAppsPerOwner; max > 0 {
		n, cerr := e.countOwner(ctx, ns, owner)
		if cerr != nil {
			return department, cerr
		}
		if n >= max {
			rejections.WithLabelValues("owner").Inc()
			return department, &ExceededError{Scope: "owner", Owner: owner, Limit: max, Current: n}
		}
	}

	if department != "" {
		if max := e.deptCeiling(department); max > 0 {
			n, cerr := e.count(ctx, ns, vibedv1.LabelDepartment, vibedv1.SanitizeLabel(department))
			if cerr != nil {
				return department, cerr
			}
			if n >= max {
				rejections.WithLabelValues("department").Inc()
				return department, &ExceededError{Scope: "department", Department: department, Limit: max, Current: n}
			}
		}
	}

	return department, nil
}

// VerifyAfterCreate re-checks the ceilings AFTER the app CR has been created, to
// close the TOCTOU window in Authorize (#75): two concurrent deploys can each
// pass Authorize while the count is still under the limit, then both create,
// briefly overshooting. Called post-create, this recounts (the new app is now
// visible) and returns an ExceededError if this owner/department/tenant is now
// over its ceiling — the caller compensates by deleting the app it just made.
//
// The check is deterministic under the race by tie-breaking on creationTimestamp
// then name: among the apps that overshot the limit, only the newest ones are
// asked to roll back, so concurrent racers don't all delete themselves (which
// would leave the owner under quota with nothing deployed). appName is the app
// just created; it rolls back only if it is among the excess (newest) entries.
func (e *Enforcer) VerifyAfterCreate(ctx context.Context, t tenant.Tenant, owner, appName string) error {
	ns := t.Namespace
	if ns == "" {
		ns = e.namespace
	}

	// Per-owner ceiling is the one most exposed to the race (a single user
	// firing concurrent deploys); tenant/department overshoot is rarer and the
	// per-owner recount is the cleanly-tractable guard.
	max := e.cfg.MaxAppsPerOwner
	if max <= 0 {
		return nil
	}

	var list vibedv1.VibedAppList
	if err := e.client.List(ctx, &list, client.InNamespace(ns),
		client.MatchingLabels{vibedv1.LabelOwner: vibedv1.SanitizeLabel(owner)}); err != nil {
		return fmt.Errorf("verify owner quota: %w", err)
	}
	// Exact-owner apps, oldest first (creationTimestamp, then name as a stable
	// tiebreak so all racers agree on the ordering).
	owned := make([]vibedv1.VibedApp, 0, len(list.Items))
	for i := range list.Items {
		if list.Items[i].Spec.Owner == owner {
			owned = append(owned, list.Items[i])
		}
	}
	if len(owned) <= max {
		return nil
	}
	slices.SortFunc(owned, func(a, b vibedv1.VibedApp) int {
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			if a.CreationTimestamp.Before(&b.CreationTimestamp) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	// The first `max` are kept; the rest are the excess. This app rolls back
	// only if it's in the excess set.
	for _, excess := range owned[max:] {
		if excess.Name == appName {
			rejections.WithLabelValues("owner").Inc()
			return &ExceededError{Scope: "owner", Owner: owner, Limit: max, Current: len(owned)}
		}
	}
	return nil
}

// deptCeiling returns the per-department override if set, else the global cap.
func (e *Enforcer) deptCeiling(dept string) int {
	if v, ok := e.cfg.PerDepartment[dept]; ok {
		return v
	}
	return e.cfg.MaxAppsPerDepartment
}

// count returns the number of live VibedApps in namespace carrying the given
// label value.
func (e *Enforcer) count(ctx context.Context, namespace, key, val string) (int, error) {
	var list vibedv1.VibedAppList
	if err := e.client.List(ctx, &list, client.InNamespace(namespace), client.MatchingLabels{key: val}); err != nil {
		return 0, fmt.Errorf("count apps by %s=%s: %w", key, val, err)
	}
	return len(list.Items), nil
}

// countOwner counts live VibedApps owned EXACTLY by owner. The owner label is a
// lossy, sanitized mirror of spec.owner (SanitizeLabel replaces invalid chars
// and truncates to 63), so two distinct owners can collide onto one label value
// — counting by the label alone would conflate their quotas (#66). We use the
// label only as an indexed pre-filter, then match the authoritative spec.owner
// exactly.
func (e *Enforcer) countOwner(ctx context.Context, namespace, owner string) (int, error) {
	var list vibedv1.VibedAppList
	if err := e.client.List(ctx, &list, client.InNamespace(namespace),
		client.MatchingLabels{vibedv1.LabelOwner: vibedv1.SanitizeLabel(owner)}); err != nil {
		return 0, fmt.Errorf("count apps by owner: %w", err)
	}
	n := 0
	for i := range list.Items {
		if list.Items[i].Spec.Owner == owner {
			n++
		}
	}
	return n, nil
}

// countAll returns the total number of live VibedApps in namespace.
func (e *Enforcer) countAll(ctx context.Context, namespace string) (int, error) {
	var list vibedv1.VibedAppList
	if err := e.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return 0, fmt.Errorf("count apps in %s: %w", namespace, err)
	}
	return len(list.Items), nil
}

// departmentFor resolves owner -> department name via the user store. Returns
// "" when there's no store, no user, or no department.
func (e *Enforcer) departmentFor(ctx context.Context, owner string) string {
	if e.users == nil {
		return ""
	}
	u, err := e.users.GetUser(ctx, owner)
	if err != nil || u == nil || u.DepartmentID == "" {
		return ""
	}
	d, err := e.users.GetDepartment(ctx, u.DepartmentID)
	if err != nil || d == nil {
		return ""
	}
	return d.Name
}
