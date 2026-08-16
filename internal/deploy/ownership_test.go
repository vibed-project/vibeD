package deploy

import (
	"bytes"
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/authz"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

func staticSource(t *testing.T) *bytes.Reader {
	t.Helper()
	return bytes.NewReader(gzTarball(t, map[string]string{"index.html": "<h1>hi</h1>"}))
}

// A redeploy mutates an existing app. Without an ownership check, deploying
// under a name someone else already used silently replaces their app's source
// and serves the attacker's code on the victim's URL.
func TestDeploy_RedeployByNonOwnerIsDenied(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())

	markReady(c, "shop", "vibed-apps", 20*time.Millisecond)
	if _, err := svc.Deploy(context.Background(), Request{
		Name: "shop", Owner: "alice@example.com", Tarball: staticSource(t),
	}); err != nil {
		t.Fatalf("alice's first deploy: %v", err)
	}

	_, err := svc.Deploy(context.Background(), Request{
		Name: "shop", Owner: "mallory@example.com", Tarball: staticSource(t),
	})
	if err == nil {
		t.Fatal("mallory took over alice's app; want a denial")
	}
	if !authz.IsDenied(err) {
		t.Errorf("err = %v, want an authorization denial (403)", err)
	}

	// Alice's app must be untouched and still hers.
	app := &vibedv1.VibedApp{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "shop", Namespace: "vibed-apps"}, app); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Spec.Owner != "alice@example.com" {
		t.Errorf("owner = %q, want alice@example.com", app.Spec.Owner)
	}
}

// The owner may of course keep redeploying their own app.
func TestDeploy_OwnerCanRedeploy(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())

	markReady(c, "shop", "vibed-apps", 20*time.Millisecond)
	if _, err := svc.Deploy(context.Background(), Request{
		Name: "shop", Owner: "alice@example.com", Tarball: staticSource(t),
	}); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if _, err := svc.Deploy(context.Background(), Request{
		Name: "shop", Owner: "alice@example.com", Tarball: staticSource(t),
	}); err != nil {
		t.Fatalf("owner redeploy was denied: %v", err)
	}
}

// An admin can redeploy anyone's app (operator break-glass).
func TestDeploy_AdminCanRedeployOthersApp(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())

	markReady(c, "shop", "vibed-apps", 20*time.Millisecond)
	if _, err := svc.Deploy(context.Background(), Request{
		Name: "shop", Owner: "alice@example.com", Tarball: staticSource(t),
	}); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	ctx := vibedauth.WithRole(context.Background(), "admin")
	markReady(c, "shop", "vibed-apps", 20*time.Millisecond)
	if _, err := svc.Deploy(ctx, Request{
		Name: "shop", Owner: "ops@example.com", Tarball: staticSource(t),
	}); err != nil {
		t.Fatalf("admin redeploy was denied: %v", err)
	}
}

// A redeploy must describe the resource by its REAL owner, otherwise an
// ownership-aware authorizer sees every request as self-owned and can never
// catch a takeover attempt.
func TestDeploy_AuthzSeesExistingOwnerOnRedeploy(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())

	markReady(c, "shop", "vibed-apps", 20*time.Millisecond)
	if _, err := svc.Deploy(context.Background(), Request{
		Name: "shop", Owner: "alice@example.com", Tarball: staticSource(t),
	}); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	var seen authz.Resource
	svc.Authz = authzFunc(func(_ context.Context, r authz.Request) error {
		seen = r.Resource
		return nil
	})
	ctx := vibedauth.WithRole(context.Background(), "admin") // pass the ownership gate
	markReady(c, "shop", "vibed-apps", 20*time.Millisecond)
	if _, err := svc.Deploy(ctx, Request{
		Name: "shop", Owner: "ops@example.com", Tarball: staticSource(t),
	}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	if seen.Owner != "alice@example.com" {
		t.Errorf("authz Resource.Owner = %q, want the existing owner alice@example.com", seen.Owner)
	}
}

type authzFunc func(context.Context, authz.Request) error

func (f authzFunc) Authorize(ctx context.Context, r authz.Request) error { return f(ctx, r) }
