package orchestrator

import (
	"context"
	"testing"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/pkg/api"
)

// newAuthConfig builds an Orchestrator carrying just enough config for the
// ownership checks. authEnabled toggles config.Auth.Enabled.
func orchWithAuth(enabled bool) *Orchestrator {
	return &Orchestrator{cfg: &config.Config{Auth: config.AuthConfig{Enabled: enabled}}}
}

// ownerCtx returns a context carrying the given user id (and admin flag).
func userCtx(id string, admin bool) context.Context {
	ctx := vibedauth.WithUserID(context.Background(), id)
	if admin {
		ctx = vibedauth.WithRole(ctx, "admin")
	}
	return ctx
}

// TestCheckOwnershipFailClosed is the core proof for the IDOR fix (#49): with
// auth ENABLED, an anonymous caller (no user in ctx) must be DENIED — this is
// the transport-without-identity case that let any artifact's logs be read and
// any artifact be rolled back. With auth DISABLED the check still passes.
func TestCheckOwnershipFailClosed(t *testing.T) {
	artifact := &api.Artifact{ID: "art-1", OwnerID: "apikey-alice"}

	// Auth enabled + anonymous caller → DENY (fail closed).
	oEnabled := orchWithAuth(true)
	if err := oEnabled.checkOwnership(context.Background(), artifact); err == nil {
		t.Error("checkOwnership: anonymous caller with auth ENABLED must be denied")
	}
	if err := oEnabled.checkWriteOwnership(context.Background(), artifact); err == nil {
		t.Error("checkWriteOwnership: anonymous caller with auth ENABLED must be denied")
	}

	// Auth disabled + anonymous caller → allow (dev/no-auth mode unchanged).
	oDisabled := orchWithAuth(false)
	if err := oDisabled.checkOwnership(context.Background(), artifact); err != nil {
		t.Errorf("checkOwnership: anonymous caller with auth DISABLED should pass, got %v", err)
	}
	if err := oDisabled.checkWriteOwnership(context.Background(), artifact); err != nil {
		t.Errorf("checkWriteOwnership: anonymous caller with auth DISABLED should pass, got %v", err)
	}
}

// TestCheckOwnershipCrossUserDenied proves a different authenticated user cannot
// read or write someone else's artifact, while the owner and an admin can.
func TestCheckOwnershipCrossUserDenied(t *testing.T) {
	artifact := &api.Artifact{ID: "art-1", OwnerID: "apikey-alice"}
	o := orchWithAuth(true)

	// Owner: allowed.
	if err := o.checkOwnership(userCtx("apikey-alice", false), artifact); err != nil {
		t.Errorf("owner read denied: %v", err)
	}
	if err := o.checkWriteOwnership(userCtx("apikey-alice", false), artifact); err != nil {
		t.Errorf("owner write denied: %v", err)
	}

	// Different user (bob): denied for both read and write.
	if err := o.checkOwnership(userCtx("apikey-bob", false), artifact); err == nil {
		t.Error("cross-user read must be denied")
	}
	if err := o.checkWriteOwnership(userCtx("apikey-bob", false), artifact); err == nil {
		t.Error("cross-user write must be denied")
	}

	// Admin: allowed for both.
	if err := o.checkOwnership(userCtx("apikey-admin", true), artifact); err != nil {
		t.Errorf("admin read denied: %v", err)
	}
	if err := o.checkWriteOwnership(userCtx("apikey-admin", true), artifact); err != nil {
		t.Errorf("admin write denied: %v", err)
	}
}

// TestCheckOwnershipSharedRead confirms a user on SharedWith gets read access
// but not write access.
func TestCheckOwnershipSharedRead(t *testing.T) {
	artifact := &api.Artifact{ID: "art-1", OwnerID: "apikey-alice", SharedWith: []string{"apikey-carol"}}
	o := orchWithAuth(true)

	if err := o.checkOwnership(userCtx("apikey-carol", false), artifact); err != nil {
		t.Errorf("shared user read denied: %v", err)
	}
	if err := o.checkWriteOwnership(userCtx("apikey-carol", false), artifact); err == nil {
		t.Error("shared user must not have write access")
	}
}
