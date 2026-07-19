package deploy

import (
	"bytes"
	"context"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// fakeShareLinkStore is an in-memory store.ShareLinkStore for tests.
type fakeShareLinkStore struct {
	links map[string]*api.ShareLink
	hash  map[string]string
}

func newFakeShareLinkStore() *fakeShareLinkStore {
	return &fakeShareLinkStore{links: map[string]*api.ShareLink{}, hash: map[string]string{}}
}

func (f *fakeShareLinkStore) CreateShareLink(_ context.Context, l *api.ShareLink, h string) error {
	cp := *l
	f.links[l.Token] = &cp
	f.hash[l.Token] = h
	return nil
}

func (f *fakeShareLinkStore) GetShareLink(_ context.Context, t string) (*api.ShareLink, string, error) {
	l, ok := f.links[t]
	if !ok {
		return nil, "", &api.ErrShareLinkNotFound{Token: t}
	}
	return l, f.hash[t], nil
}

func (f *fakeShareLinkStore) ListShareLinks(_ context.Context, artifactID string, limit, offset int) ([]api.ShareLink, error) {
	var out []api.ShareLink
	for _, l := range f.links {
		if l.ArtifactID == artifactID {
			out = append(out, *l)
		}
	}
	if offset > 0 && offset < len(out) {
		out = out[offset:]
	} else if offset >= len(out) {
		out = nil
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeShareLinkStore) RevokeShareLink(_ context.Context, t string) error {
	if l, ok := f.links[t]; ok {
		l.Revoked = true
	}
	return nil
}

func TestShareLinks(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.DeployTimeout = 50 * time.Millisecond
	svc.ShareLinks = newFakeShareLinkStore()
	svc.BaseURL = "https://apps.example.test"

	if _, err := svc.Deploy(context.Background(), Request{
		Name: "shareapp", Owner: "alice@example.com",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "<h1>hi</h1>"})),
	}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	// Give the app a URL so resolve returns it.
	app := &vibedv1.VibedApp{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "shareapp", Namespace: "vibed-apps"}, app); err != nil {
		t.Fatal(err)
	}
	app.Status.URL = "https://abc.apps.example.test"
	if err := c.Status().Update(context.Background(), app); err != nil {
		t.Fatal(err)
	}

	link, err := svc.CreateShareLink(context.Background(), "alice@example.com", "shareapp", "secret", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(link.Token) != 64 || !link.HasPassword || link.URL == "" {
		t.Fatalf("unexpected link: %+v", link)
	}

	// A non-owner cannot create a link (ownership-checked, no existence leak).
	if _, err := svc.CreateShareLink(context.Background(), "bob@example.com", "shareapp", "", 0); err == nil {
		t.Fatal("expected error for non-owner create")
	}

	links, err := svc.ListShareLinks(context.Background(), "alice@example.com", "shareapp", 0, 0)
	if err != nil || len(links) != 1 {
		t.Fatalf("list: err=%v n=%d", err, len(links))
	}

	// Resolve requires the password, then returns the app.
	if _, err := svc.ResolveShareLink(context.Background(), link.Token, ""); err == nil {
		t.Fatal("expected password-required error")
	}
	got, err := svc.ResolveShareLink(context.Background(), link.Token, "secret")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Status.URL != "https://abc.apps.example.test" {
		t.Fatalf("resolve URL = %q", got.Status.URL)
	}

	// Revoked links no longer resolve.
	if err := svc.RevokeShareLink(context.Background(), "alice@example.com", link.Token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.ResolveShareLink(context.Background(), link.Token, "secret"); err == nil {
		t.Fatal("expected not-found after revoke")
	}
}
