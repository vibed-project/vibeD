package orchestrator

import (
	"context"
	"testing"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/pkg/api"
)

func orchWithAuth(enabled bool) *Orchestrator {
	return &Orchestrator{cfg: &config.Config{Auth: config.AuthConfig{Enabled: enabled}}}
}

func ctxUser(id string) context.Context {
	return vibedauth.WithUserID(context.Background(), id)
}

func ctxAdmin(id string) context.Context {
	return vibedauth.WithRole(vibedauth.WithUserID(context.Background(), id), "admin")
}

// TestCheckOwnership_FailsClosedWhenAuthEnabled is the #49 regression guard: an
// identity-less caller (e.g. MCP over stdio, which doesn't propagate a token)
// must NOT be able to read another user's artifact when auth is enabled. The
// bug keyed the bypass on an empty owner ID, which treated "no identity" as
// trusted.
func TestCheckOwnership_FailsClosedWhenAuthEnabled(t *testing.T) {
	art := &api.Artifact{ID: "app-1", OwnerID: "alice", SharedWith: []string{"carol"}}

	// Auth disabled → no enforcement (dev/no-auth mode).
	if err := orchWithAuth(false).checkOwnership(context.Background(), art); err != nil {
		t.Errorf("auth disabled: want allow, got %v", err)
	}

	on := orchWithAuth(true)
	cases := []struct {
		name    string
		ctx     context.Context
		allowed bool
	}{
		{"anonymous (auth on) denied", context.Background(), false}, // the fix
		{"owner allowed", ctxUser("alice"), true},
		{"non-owner denied", ctxUser("mallory"), false},
		{"shared user allowed", ctxUser("carol"), true},
		{"admin allowed", ctxAdmin("someadmin"), true},
	}
	for _, c := range cases {
		err := on.checkOwnership(c.ctx, art)
		if c.allowed && err != nil {
			t.Errorf("%s: want allow, got %v", c.name, err)
		}
		if !c.allowed && err == nil {
			t.Errorf("%s: want deny, got allow", c.name)
		}
	}
}

func TestCheckWriteOwnership_FailsClosedWhenAuthEnabled(t *testing.T) {
	art := &api.Artifact{ID: "app-1", OwnerID: "alice", SharedWith: []string{"carol"}}
	on := orchWithAuth(true)

	cases := []struct {
		name    string
		ctx     context.Context
		allowed bool
	}{
		{"anonymous (auth on) denied", context.Background(), false}, // the fix
		{"owner allowed", ctxUser("alice"), true},
		{"non-owner denied", ctxUser("mallory"), false},
		{"shared user cannot write", ctxUser("carol"), false}, // read-only sharing
		{"admin allowed", ctxAdmin("someadmin"), true},
	}
	for _, c := range cases {
		err := on.checkWriteOwnership(c.ctx, art)
		if c.allowed && err != nil {
			t.Errorf("%s: want allow, got %v", c.name, err)
		}
		if !c.allowed && err == nil {
			t.Errorf("%s: want deny, got allow", c.name)
		}
	}

	// Auth disabled → allow even anonymously.
	if err := orchWithAuth(false).checkWriteOwnership(context.Background(), art); err != nil {
		t.Errorf("auth disabled: want allow, got %v", err)
	}
	// Unowned artifact + authenticated user → writable.
	unowned := &api.Artifact{ID: "app-2", OwnerID: ""}
	if err := on.checkWriteOwnership(ctxUser("alice"), unowned); err != nil {
		t.Errorf("unowned artifact: want allow for authed user, got %v", err)
	}
}
