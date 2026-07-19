package mcp

import (
	"context"
	"testing"

	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// shareService is liveService plus a share-link store and public base URL.
func shareService(t *testing.T, apps ...*vibedv1.VibedApp) (*deploy.Service, *fakeShareLinkStore) {
	t.Helper()
	svc := liveService(t, apps...)
	sls := newFakeShareLinkStore()
	svc.ShareLinks = sls
	svc.BaseURL = "https://vibed.test"
	return svc, sls
}

func TestCreateShareLinkLive(t *testing.T) {
	svc, sls := shareService(t, liveApp("app1", localOwner, "https://app1.vibed.test", vibedv1.PhaseReady, nil))
	cs := newSession(t, nil, svc) // nil orch: the live path must never touch it

	out := callTool(t, cs, "create_share_link", map[string]any{"artifact_id": "app1", "password": "s3cret", "expires_in": "24h"})
	assertKeys(t, out, "token", "artifact_id", "created_by", "has_password", "expires_at", "created_at", "revoked", "url")
	if out["artifact_id"] != "app1" || out["created_by"] != localOwner || out["has_password"] != true {
		t.Errorf("unexpected link fields: %#v", out)
	}
	token, _ := out["token"].(string)
	if out["url"] != "https://vibed.test/share/"+token {
		t.Errorf("url = %v, want BaseURL-derived share URL", out["url"])
	}
	if _, _, err := sls.GetShareLink(context.Background(), token); err != nil {
		t.Errorf("link not persisted in the deploy service's store: %v", err)
	}
}

func TestCreateShareLinkLiveOwnership(t *testing.T) {
	svc, _ := shareService(t, liveApp("bobapp", "bob", "", vibedv1.PhaseReady, nil))
	cs := newSession(t, nil, svc)

	callToolExpectError(t, cs, "create_share_link", map[string]any{"artifact_id": "bobapp"})
}

func TestListShareLinksLive(t *testing.T) {
	svc, _ := shareService(t, liveApp("app1", localOwner, "", vibedv1.PhaseReady, nil))
	// Seed two links through the service itself so list sees real records.
	for range 2 {
		if _, err := svc.CreateShareLink(context.Background(), localOwner, "app1", "", 0); err != nil {
			t.Fatalf("CreateShareLink: %v", err)
		}
	}
	cs := newSession(t, nil, svc)

	out := callTool(t, cs, "list_share_links", map[string]any{"artifact_id": "app1"})
	assertKeys(t, out, "links")
	links, ok := out["links"].([]any)
	if !ok || len(links) != 2 {
		t.Fatalf("links = %#v, want 2 entries", out["links"])
	}
	entry := links[0].(map[string]any)
	assertKeys(t, entry, "token", "artifact_id", "created_by", "has_password", "created_at", "revoked", "url")
}

func TestListShareLinksLiveEmpty(t *testing.T) {
	svc, _ := shareService(t, liveApp("app1", localOwner, "", vibedv1.PhaseReady, nil))
	cs := newSession(t, nil, svc)

	out := callTool(t, cs, "list_share_links", map[string]any{"artifact_id": "app1"})
	if links, ok := out["links"].([]any); !ok || len(links) != 0 {
		t.Fatalf("links = %#v, want empty array (not null)", out["links"])
	}
}

func TestRevokeShareLinkLive(t *testing.T) {
	svc, sls := shareService(t, liveApp("app1", localOwner, "", vibedv1.PhaseReady, nil))
	link, err := svc.CreateShareLink(context.Background(), localOwner, "app1", "", 0)
	if err != nil {
		t.Fatalf("CreateShareLink: %v", err)
	}
	cs := newSession(t, nil, svc)

	out := callTool(t, cs, "revoke_share_link", map[string]any{"token": link.Token})
	assertKeys(t, out, "status")
	if out["status"] != "revoked" {
		t.Errorf("status = %v, want revoked", out["status"])
	}
	if l, _, _ := sls.GetShareLink(context.Background(), link.Token); l == nil || !l.Revoked {
		t.Errorf("link not revoked in the deploy service's store")
	}
}

func TestRevokeShareLinkLiveOwnership(t *testing.T) {
	// A link pointing at someone else's app must not be revokable by "local".
	svc, sls := shareService(t, liveApp("bobapp", "bob", "", vibedv1.PhaseReady, nil))
	if err := sls.CreateShareLink(context.Background(), &api.ShareLink{Token: "tok-bob", ArtifactID: "bobapp", CreatedBy: "bob"}, ""); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	cs := newSession(t, nil, svc)

	callToolExpectError(t, cs, "revoke_share_link", map[string]any{"token": "tok-bob"})
	if l, _, _ := sls.GetShareLink(context.Background(), "tok-bob"); l == nil || l.Revoked {
		t.Errorf("cross-owner revoke must not flip the link")
	}
}

func TestCreateShareLinkFallback(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	if err := st.Create(ctx, &api.Artifact{ID: "legacy1", Name: "legacy1", Status: api.StatusRunning, Target: api.TargetKubernetes}); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	sls := newFakeShareLinkStore()
	cs := newSession(t, fallbackOrch(st, sls, deployer.NewFactory()), nil)

	out := callTool(t, cs, "create_share_link", map[string]any{"artifact_id": "legacy1"})
	// No BaseURL on the legacy test config → no url key; no expiry → no
	// expires_at key. Same schema either way (both are omitempty).
	assertKeys(t, out, "token", "artifact_id", "created_by", "has_password", "created_at", "revoked")
	if out["artifact_id"] != "legacy1" {
		t.Errorf("artifact_id = %v, want legacy1", out["artifact_id"])
	}
	token, _ := out["token"].(string)
	if _, _, err := sls.GetShareLink(ctx, token); err != nil {
		t.Errorf("fallback did not write the orchestrator's share-link store: %v", err)
	}
}
