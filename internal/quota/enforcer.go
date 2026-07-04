// Package quota enforces per-owner and per-department caps on how many
// concurrent apps may be deployed (enterprise governance). The Enforcer is
// consulted by the deploy path before a NEW VibedApp is created; redeploys of
// an existing app are never gated. Counts are taken over live VibedApps via
// the vibed.dev/owner and vibed.dev/department labels the deploy path stamps.
package quota

import (
	"context"
	"fmt"

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
		n, cerr := e.count(ctx, ns, vibedv1.LabelOwner, vibedv1.SanitizeLabel(owner))
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
