package quota

import (
	"context"
	"errors"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/tenant"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

const ns = "vibed-apps"

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := vibedv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

// app builds a labeled VibedApp the enforcer can count.
func app(name, owner, dept string) *vibedv1.VibedApp {
	labels := map[string]string{vibedv1.LabelOwner: vibedv1.SanitizeLabel(owner)}
	if dept != "" {
		labels[vibedv1.LabelDepartment] = vibedv1.SanitizeLabel(dept)
	}
	return &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec:       vibedv1.VibedAppSpec{Owner: owner},
	}
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()
}

// fakeUsers maps an owner to a department name.
type fakeUsers struct {
	deptOf map[string]string
}

func (f *fakeUsers) GetUser(_ context.Context, id string) (*api.User, error) {
	d, ok := f.deptOf[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return &api.User{ID: id, DepartmentID: "dept-" + d}, nil
}

func (f *fakeUsers) GetDepartment(_ context.Context, id string) (*api.Department, error) {
	return &api.Department{ID: id, Name: id[len("dept-"):]}, nil
}

func TestPerOwnerCeiling(t *testing.T) {
	c := newClient(t, app("a", "alice", ""), app("b", "alice", ""))
	e := NewEnforcer(c, nil, ns, config.QuotasConfig{Enabled: true, MaxAppsPerOwner: 2})

	_, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, "alice", true)
	var qe *ExceededError
	if !errors.As(err, &qe) || qe.Scope != "owner" {
		t.Fatalf("alice at limit: want owner ExceededError, got %v", err)
	}
	if _, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, "bob", true); err != nil {
		t.Fatalf("bob (0 apps) should be allowed: %v", err)
	}
}

// TestPerOwnerCeilingNoLabelCollision guards issue #66: two owners whose
// identities sanitize to the SAME vibed.dev/owner label ("alice@corp" and
// "alice_corp" both -> "alice_corp") must not share a quota bucket. alice@corp
// is at its limit; alice_corp (0 apps of its own) must still be allowed, and
// alice@corp must be counted against only its own apps.
func TestPerOwnerCeilingNoLabelCollision(t *testing.T) {
	const a, b = "alice@corp", "alice_corp"
	if vibedv1.SanitizeLabel(a) != vibedv1.SanitizeLabel(b) {
		t.Fatalf("test premise broken: %q and %q must sanitize to the same label", a, b)
	}
	// Two apps owned by alice@corp, zero owned by alice_corp.
	c := newClient(t, app("x", a, ""), app("y", a, ""))
	e := NewEnforcer(c, nil, ns, config.QuotasConfig{Enabled: true, MaxAppsPerOwner: 2})

	// alice@corp is at 2/2 -> rejected.
	_, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, a, true)
	var qe *ExceededError
	if !errors.As(err, &qe) || qe.Scope != "owner" || qe.Current != 2 {
		t.Fatalf("%s at limit: want owner ExceededError current=2, got %v", a, err)
	}

	// alice_corp owns 0 apps despite the label collision -> must be allowed.
	if _, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, b, true); err != nil {
		t.Fatalf("%s (0 apps, colliding label) should be allowed, got %v", b, err)
	}
}

// TestPerOwnerCeilingNoOvershootUnderConcurrency is the #75 regression: many
// deploys for the same owner authorize concurrently against a fresh (0-app)
// store, so every call's List returns 0. Without the reservation ledger they
// would all read "below ceiling" and overshoot; with it, exactly `limit` are
// authorized because each reservation is visible to the next check.
func TestPerOwnerCeilingNoOvershootUnderConcurrency(t *testing.T) {
	const limit = 3
	const goroutines = 20
	c := newClient(t) // empty: List always returns 0 during the race
	e := NewEnforcer(c, nil, ns, config.QuotasConfig{Enabled: true, MaxAppsPerOwner: limit})

	var wg sync.WaitGroup
	oks := make([]bool, goroutines)
	releases := make([]func(), goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, release, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, "alice", true)
			releases[i] = release
			oks[i] = err == nil
		}(i)
	}
	wg.Wait()

	authorized := 0
	for _, ok := range oks {
		if ok {
			authorized++
		}
	}
	if authorized != limit {
		t.Fatalf("concurrent deploys authorized = %d, want exactly %d (reservations must bound it to the ceiling)", authorized, limit)
	}

	// Releasing every reservation frees the slots again (nothing was created).
	for _, rel := range releases {
		if rel != nil {
			rel()
		}
	}
	if _, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, "alice", true); err != nil {
		t.Fatalf("after releasing all reservations a new deploy must be allowed, got %v", err)
	}
}

// TestReservationReleasedOnRejection makes sure a deploy that clears the owner
// ceiling but trips a later (department) ceiling doesn't leak the owner-scope
// reservation it already took.
func TestReservationReleasedOnRejection(t *testing.T) {
	users := &fakeUsers{deptOf: map[string]string{"carol": "platform"}}
	// Owner ceiling 5 (room), department ceiling 1 with one app already present.
	c := newClient(t, app("existing", "someoneelse", "platform"))
	e := NewEnforcer(c, users, ns, config.QuotasConfig{
		Enabled: true, MaxAppsPerOwner: 5,
		PerDepartment: map[string]int{"platform": 1},
	})

	// carol trips the department ceiling → rejected, but her owner reservation
	// must be released. Do it a few times; a leak would accumulate and eventually
	// (wrongly) trip the owner ceiling of 5.
	for i := 0; i < 8; i++ {
		_, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, "carol", true)
		var qe *ExceededError
		if !errors.As(err, &qe) || qe.Scope != "department" {
			t.Fatalf("iter %d: want department ExceededError, got %v", i, err)
		}
	}
}

func TestRedeploySkipsCeiling(t *testing.T) {
	c := newClient(t, app("a", "alice", ""), app("b", "alice", ""))
	e := NewEnforcer(c, nil, ns, config.QuotasConfig{Enabled: true, MaxAppsPerOwner: 2})

	if _, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, "alice", false); err != nil {
		t.Fatalf("redeploy (isNew=false) must skip the ceiling: %v", err)
	}
}

func TestPerDepartmentCeiling(t *testing.T) {
	c := newClient(t, app("a", "alice", "platform"), app("b", "bob", "platform"))
	users := &fakeUsers{deptOf: map[string]string{"carol": "platform"}}
	e := NewEnforcer(c, users, ns, config.QuotasConfig{Enabled: true, MaxAppsPerDepartment: 2})

	dept, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, "carol", true)
	if dept != "platform" {
		t.Fatalf("department resolution = %q, want platform", dept)
	}
	var qe *ExceededError
	if !errors.As(err, &qe) || qe.Scope != "department" {
		t.Fatalf("platform at 2/2: want department ExceededError, got %v", err)
	}
}

func TestPerDepartmentOverride(t *testing.T) {
	c := newClient(t, app("a", "alice", "platform"), app("b", "bob", "platform"))
	users := &fakeUsers{deptOf: map[string]string{"carol": "platform"}}
	cfg := config.QuotasConfig{Enabled: true, MaxAppsPerDepartment: 2, PerDepartment: map[string]int{"platform": 5}}
	e := NewEnforcer(c, users, ns, cfg)

	if _, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, "carol", true); err != nil {
		t.Fatalf("override should raise platform cap to 5: %v", err)
	}
}

// appNs builds an owner-labeled app in a specific namespace.
func appNs(namespace, name, owner string) *vibedv1.VibedApp {
	return &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{vibedv1.LabelOwner: vibedv1.SanitizeLabel(owner)}},
		Spec:       vibedv1.VibedAppSpec{Owner: owner},
	}
}

func TestTenantCeiling(t *testing.T) {
	c := newClient(t, app("a", "alice", ""), app("b", "bob", ""))
	e := NewEnforcer(c, nil, ns, config.QuotasConfig{Enabled: true})

	// A tenant total cap of 2 with 2 apps already in the namespace -> rejected.
	_, _, err := e.Authorize(context.Background(), tenant.Tenant{ID: "t1", Namespace: ns, Limits: tenant.Limits{MaxApps: 2}}, "carol", true)
	var qe *ExceededError
	if !errors.As(err, &qe) || qe.Scope != "tenant" {
		t.Fatalf("tenant at 2/2: want tenant ExceededError, got %v", err)
	}
	// A different, empty tenant namespace is allowed under the same cap.
	if _, _, err := e.Authorize(context.Background(), tenant.Tenant{ID: "t2", Namespace: "other-ns", Limits: tenant.Limits{MaxApps: 2}}, "carol", true); err != nil {
		t.Fatalf("an empty tenant should be allowed: %v", err)
	}
}

// Ceilings are scoped to the tenant's namespace: one tenant's apps never count
// against another's.
func TestCeilingsAreTenantNamespaceScoped(t *testing.T) {
	c := newClient(t, appNs("tenant-a", "a", "alice"), appNs("tenant-a", "b", "alice"))
	e := NewEnforcer(c, nil, ns, config.QuotasConfig{Enabled: true, MaxAppsPerOwner: 2})

	if _, _, err := e.Authorize(context.Background(), tenant.Tenant{ID: "a", Namespace: "tenant-a"}, "alice", true); err == nil {
		t.Fatal("alice should be at her per-owner cap within tenant-a")
	}
	if _, _, err := e.Authorize(context.Background(), tenant.Tenant{ID: "b", Namespace: "tenant-b"}, "alice", true); err != nil {
		t.Fatalf("alice's tenant-a apps must not count in tenant-b: %v", err)
	}
}

func TestNoResolverSkipsDepartmentGate(t *testing.T) {
	c := newClient(t, app("a", "alice", "platform"))
	e := NewEnforcer(c, nil, ns, config.QuotasConfig{Enabled: true, MaxAppsPerDepartment: 1})

	// No user resolver -> department unknown -> only the (unset) per-owner cap
	// applies, so the deploy is allowed.
	if _, _, err := e.Authorize(context.Background(), tenant.Tenant{Namespace: ns}, "bob", true); err != nil {
		t.Fatalf("missing resolver should skip the department gate: %v", err)
	}
}
