package quota

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/config"
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

	_, err := e.Authorize(context.Background(), "alice", true)
	var qe *ExceededError
	if !errors.As(err, &qe) || qe.Scope != "owner" {
		t.Fatalf("alice at limit: want owner ExceededError, got %v", err)
	}
	if _, err := e.Authorize(context.Background(), "bob", true); err != nil {
		t.Fatalf("bob (0 apps) should be allowed: %v", err)
	}
}

func TestRedeploySkipsCeiling(t *testing.T) {
	c := newClient(t, app("a", "alice", ""), app("b", "alice", ""))
	e := NewEnforcer(c, nil, ns, config.QuotasConfig{Enabled: true, MaxAppsPerOwner: 2})

	if _, err := e.Authorize(context.Background(), "alice", false); err != nil {
		t.Fatalf("redeploy (isNew=false) must skip the ceiling: %v", err)
	}
}

func TestPerDepartmentCeiling(t *testing.T) {
	c := newClient(t, app("a", "alice", "platform"), app("b", "bob", "platform"))
	users := &fakeUsers{deptOf: map[string]string{"carol": "platform"}}
	e := NewEnforcer(c, users, ns, config.QuotasConfig{Enabled: true, MaxAppsPerDepartment: 2})

	dept, err := e.Authorize(context.Background(), "carol", true)
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

	if _, err := e.Authorize(context.Background(), "carol", true); err != nil {
		t.Fatalf("override should raise platform cap to 5: %v", err)
	}
}

func TestNoResolverSkipsDepartmentGate(t *testing.T) {
	c := newClient(t, app("a", "alice", "platform"))
	e := NewEnforcer(c, nil, ns, config.QuotasConfig{Enabled: true, MaxAppsPerDepartment: 1})

	// No user resolver -> department unknown -> only the (unset) per-owner cap
	// applies, so the deploy is allowed.
	if _, err := e.Authorize(context.Background(), "bob", true); err != nil {
		t.Fatalf("missing resolver should skip the department gate: %v", err)
	}
}
