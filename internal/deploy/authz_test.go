package deploy

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/authz"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// stubAuthorizer is a minimal authz.Authorizer for exercising the deploy
// service's authorization wiring: owners control their own existing apps;
// listed "readers" may read any app; listed "writers" may write any app.
type stubAuthorizer struct {
	readers map[string]bool
	writers map[string]bool
}

func (s stubAuthorizer) Authorize(_ context.Context, req authz.Request) error {
	// Owner controls their own existing resource (deploy excluded — creation).
	if req.Action != authz.ActionAppDeploy && req.Resource.Owner != "" && req.Resource.Owner == req.Subject {
		return nil
	}
	switch req.Action {
	case authz.ActionAppGet, authz.ActionAppList:
		if s.readers[req.Subject] {
			return nil
		}
	default:
		if s.writers[req.Subject] {
			return nil
		}
	}
	return &authz.DeniedError{Action: req.Action, Reason: "stub deny"}
}

func appNames(apps []vibedv1.VibedApp) []string {
	out := make([]string, len(apps))
	for i, a := range apps {
		out[i] = a.Name
	}
	return out
}

// TestListScopingWithAuthorizer covers #21: an Authorizer expands List to the
// apps the caller may read (team-scoped visibility), replacing owner-only.
func TestListScopingWithAuthorizer(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "vibed-apps"}, Spec: vibedv1.VibedAppSpec{Owner: "alice"}},
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "vibed-apps"}, Spec: vibedv1.VibedAppSpec{Owner: "bob"}},
		).Build()
	svc := newService(c, newFakeStore())
	svc.Authz = stubAuthorizer{readers: map[string]bool{"carol": true}}

	// carol is a team reader (a Viewer): sees both apps read-only.
	if list, err := svc.List(context.Background(), "carol"); err != nil || len(list) != 2 {
		t.Fatalf("carol List = %v (err %v), want 2", appNames(list), err)
	}
	// dave has no read grant and owns nothing: sees none.
	if list, err := svc.List(context.Background(), "dave"); err != nil || len(list) != 0 {
		t.Fatalf("dave List = %v (err %v), want 0", appNames(list), err)
	}
	// alice still sees her own app via ownership.
	list, err := svc.List(context.Background(), "alice")
	if err != nil || len(list) != 1 || list[0].Name != "a" {
		t.Fatalf("alice List = %v (err %v), want [a]", appNames(list), err)
	}
}

// TestWriteAuthorizationWithAuthorizer covers the read/write split: a teammate
// who may read (Get succeeds) is still denied a write action (Delete → 403).
func TestWriteAuthorizationWithAuthorizer(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "vibed-apps"}, Spec: vibedv1.VibedAppSpec{Owner: "bob"}},
		).Build()
	svc := newService(c, newFakeStore())
	svc.Authz = stubAuthorizer{readers: map[string]bool{"carol": true}}

	// carol may read bob's app...
	if _, err := svc.Get(context.Background(), "carol", "b"); err != nil {
		t.Fatalf("carol Get b: want allow, got %v", err)
	}
	// ...but not delete it: a Forbidden authz denial.
	if err := svc.Delete(context.Background(), "carol", "b"); !authz.IsDenied(err) {
		t.Fatalf("carol Delete b: want denied, got %v", err)
	}
	// The app is still there.
	if _, err := svc.Get(context.Background(), "bob", "b"); err != nil {
		t.Fatalf("b should still exist: %v", err)
	}
}
