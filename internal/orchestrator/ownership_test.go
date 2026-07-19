package orchestrator

import (
	"context"
	"errors"
	"testing"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/authz"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/pkg/api"
)

// stubAuthz allows exactly the actions in its set.
type stubAuthz struct{ allow map[authz.Action]bool }

func (s stubAuthz) Authorize(_ context.Context, req authz.Request) error {
	if s.allow[req.Action] {
		return nil
	}
	return &authz.DeniedError{Action: req.Action, Reason: "stub"}
}

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
	if err := orchWithAuth(false).checkOwnership(context.Background(), art, authz.ActionAppGet); err != nil {
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
		err := on.checkOwnership(c.ctx, art, authz.ActionAppGet)
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
		err := on.checkWriteOwnership(c.ctx, art, authz.ActionAppDelete)
		if c.allowed && err != nil {
			t.Errorf("%s: want allow, got %v", c.name, err)
		}
		if !c.allowed && err == nil {
			t.Errorf("%s: want deny, got allow", c.name)
		}
	}

	// Auth disabled → allow even anonymously.
	if err := orchWithAuth(false).checkWriteOwnership(context.Background(), art, authz.ActionAppDelete); err != nil {
		t.Errorf("auth disabled: want allow, got %v", err)
	}
	// Unowned artifact + authenticated user → writable.
	unowned := &api.Artifact{ID: "app-2", OwnerID: ""}
	if err := on.checkWriteOwnership(ctxUser("alice"), unowned, authz.ActionAppDelete); err != nil {
		t.Errorf("unowned artifact: want allow for authed user, got %v", err)
	}
}

// TestCheckOwnership_UsesAuthorizerWhenSet verifies the installed authorizer
// replaces the built-in owner check on the legacy path: a non-owner is allowed
// a read the authorizer permits, and a denied write is reported as ErrNotFound
// (this path hides existence rather than returning 403).
func TestCheckOwnership_UsesAuthorizerWhenSet(t *testing.T) {
	art := &api.Artifact{ID: "app-1", OwnerID: "alice"}
	on := orchWithAuth(true)
	on.authorizer = stubAuthz{allow: map[authz.Action]bool{authz.ActionAppGet: true}}

	// Non-owner bob may read (authorizer grants app.get).
	if err := on.checkOwnership(ctxUser("bob"), art, authz.ActionAppGet); err != nil {
		t.Errorf("authorizer-allowed read: want allow, got %v", err)
	}
	// bob may not delete (authorizer denies app.delete) → ErrNotFound.
	err := on.checkWriteOwnership(ctxUser("bob"), art, authz.ActionAppDelete)
	if err == nil {
		t.Fatal("authorizer-denied write: want deny, got allow")
	}
	var nf *api.ErrNotFound
	if !errors.As(err, &nf) {
		t.Errorf("want ErrNotFound on denied legacy write, got %T (%v)", err, err)
	}
}
