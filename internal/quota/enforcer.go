// Package quota enforces per-owner and per-department caps on how many
// concurrent apps may be deployed (enterprise governance). The Enforcer is
// consulted by the deploy path before a NEW VibedApp is created; redeploys of
// an existing app are never gated. Counts are taken over live VibedApps via
// the vibed.dev/owner and vibed.dev/department labels the deploy path stamps.
package quota

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/tenant"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// defaultReservationTTL backstops a reservation whose release is never called
// (a caller bug): normal deploys release promptly once the app is List-visible,
// so this only bounds the blast radius of a leak. Generous enough to outlast a
// slow tarball upload window between the count check and the Create.
const defaultReservationTTL = 2 * time.Minute

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

	// reservations bridge the TOCTOU window between the count check and the new
	// app becoming List-visible, so concurrent deploys can't all read a
	// below-ceiling count and overshoot the ceiling (#75). Keyed by scope, each
	// entry is an in-flight deploy's expiry (a TTL leak backstop).
	reservationTTL time.Duration
	mu             sync.Mutex
	nextResID      uint64
	reservations   map[resKey]map[uint64]int64 // scope -> reservationID -> expiry unixNano
}

// resKey scopes reservations the same way the ceilings are scoped: per
// namespace + scope (tenant|owner|department) + identity.
type resKey struct{ ns, scope, id string }

// NewEnforcer builds an Enforcer over the apps namespace.
func NewEnforcer(c client.Client, users UserResolver, namespace string, cfg config.QuotasConfig) *Enforcer {
	return &Enforcer{
		client:         c,
		users:          users,
		namespace:      namespace,
		cfg:            cfg,
		reservationTTL: defaultReservationTTL,
		reservations:   map[resKey]map[uint64]int64{},
	}
}

// tryReserve atomically checks listed+reserved < max for key and, when there is
// room, records a reservation and returns its release with ok=true. Otherwise
// ok=false and the current total (listed + live reservations). The List that
// produced `listed` happens outside e.mu (it's remote I/O); reservations account
// for concurrent in-flight deploys not yet reflected in listed. The returned
// release is idempotent.
func (e *Enforcer) tryReserve(key resKey, listed, max int, now time.Time) (release func(), total int, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	total = listed + e.reservedCountLocked(key, now)
	if total >= max {
		return func() {}, total, false
	}

	e.nextResID++
	id := e.nextResID
	m := e.reservations[key]
	if m == nil {
		m = map[uint64]int64{}
		e.reservations[key] = m
	}
	m[id] = now.Add(e.reservationTTL).UnixNano()

	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			if mm := e.reservations[key]; mm != nil {
				delete(mm, id)
				if len(mm) == 0 {
					delete(e.reservations, key)
				}
			}
		})
	}, total + 1, true
}

// reservedCountLocked returns the number of unexpired reservations for key,
// pruning expired ones. Caller holds e.mu.
func (e *Enforcer) reservedCountLocked(key resKey, now time.Time) int {
	m := e.reservations[key]
	if m == nil {
		return 0
	}
	nowNano := now.UnixNano()
	for id, exp := range m {
		if exp <= nowNano {
			delete(m, id)
		}
	}
	if len(m) == 0 {
		delete(e.reservations, key)
		return 0
	}
	return len(m)
}

// Authorize resolves owner's department (returned so the caller can label the
// app) and, when isNew, rejects the deploy if it would exceed the tenant's total
// ceiling or the per-owner/per-department ceiling. All counts are scoped to the
// tenant's namespace (falling back to the enforcer's default namespace when the
// tenant leaves it empty), so ceilings are enforced per tenant. Redeploys
// (isNew=false) skip the ceiling checks but still return the department.
//
// On success it returns a non-nil release the caller MUST invoke once the new
// app is durably created (List-visible) or the deploy has failed. The release
// drops the reservation this call took against each ceiling; holding it across
// the count-check→Create window is what stops concurrent deploys from all
// reading a below-ceiling count and overshooting (#75). release is always
// non-nil (a no-op on the redeploy / rejection paths) so callers can defer it
// unconditionally.
func (e *Enforcer) Authorize(ctx context.Context, t tenant.Tenant, owner string, isNew bool) (department string, release func(), err error) {
	noop := func() {}
	ns := t.Namespace
	if ns == "" {
		ns = e.namespace
	}

	department = e.departmentFor(ctx, owner)
	if !isNew {
		return department, noop, nil
	}

	now := time.Now()
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}

	// Per-tenant total ceiling (all apps in the tenant namespace).
	if max := t.Limits.MaxApps; max > 0 {
		n, cerr := e.countAll(ctx, ns)
		if cerr != nil {
			releaseAll()
			return department, noop, cerr
		}
		rel, total, ok := e.tryReserve(resKey{ns, "tenant", t.ID}, n, max, now)
		if !ok {
			releaseAll()
			rejections.WithLabelValues("tenant").Inc()
			return department, noop, &ExceededError{Scope: "tenant", Tenant: t.ID, Limit: max, Current: total}
		}
		releases = append(releases, rel)
	}

	if max := e.cfg.MaxAppsPerOwner; max > 0 {
		n, cerr := e.countByOwner(ctx, ns, owner)
		if cerr != nil {
			releaseAll()
			return department, noop, cerr
		}
		rel, total, ok := e.tryReserve(resKey{ns, "owner", owner}, n, max, now)
		if !ok {
			releaseAll()
			rejections.WithLabelValues("owner").Inc()
			return department, noop, &ExceededError{Scope: "owner", Owner: owner, Limit: max, Current: total}
		}
		releases = append(releases, rel)
	}

	if department != "" {
		if max := e.deptCeiling(department); max > 0 {
			n, cerr := e.count(ctx, ns, vibedv1.LabelDepartment, vibedv1.SanitizeLabel(department))
			if cerr != nil {
				releaseAll()
				return department, noop, cerr
			}
			rel, total, ok := e.tryReserve(resKey{ns, "department", department}, n, max, now)
			if !ok {
				releaseAll()
				rejections.WithLabelValues("department").Inc()
				return department, noop, &ExceededError{Scope: "department", Department: department, Limit: max, Current: total}
			}
			releases = append(releases, rel)
		}
	}

	return department, releaseAll, nil
}

// deptCeiling returns the per-department override if set, else the global cap.
func (e *Enforcer) deptCeiling(dept string) int {
	if v, ok := e.cfg.PerDepartment[dept]; ok {
		return v
	}
	return e.cfg.MaxAppsPerDepartment
}

// countByOwner counts live VibedApps in namespace owned by exactly `owner`.
//
// The vibed.dev/owner label is a sanitized, ≤63-char (lossy) mirror of
// spec.owner, so distinct identities can collide onto one label value — e.g.
// "alice@corp" and "alice_corp", or two OIDC subjects sharing a 63-char prefix.
// Counting by the label alone would let colliding owners share a quota bucket
// (a cross-account DoS plus a partial quota bypass, issue #66). We therefore use
// the label only as a coarse server-side pre-filter — a collision can only add
// extra candidates, never drop a genuine match — and then count the apps whose
// exact spec.owner equals owner, which is the authoritative identity.
func (e *Enforcer) countByOwner(ctx context.Context, namespace, owner string) (int, error) {
	var list vibedv1.VibedAppList
	if err := e.client.List(ctx, &list, client.InNamespace(namespace),
		client.MatchingLabels{vibedv1.LabelOwner: vibedv1.SanitizeLabel(owner)}); err != nil {
		return 0, fmt.Errorf("count apps by owner %q: %w", owner, err)
	}
	n := 0
	for i := range list.Items {
		if list.Items[i].Spec.Owner == owner {
			n++
		}
	}
	return n, nil
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
