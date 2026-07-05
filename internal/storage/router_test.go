package storage

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"
)

// fakeStorage is a no-op Storage used as the router's fallback.
type fakeStorage struct{ name string }

func (f *fakeStorage) StoreSource(context.Context, string, map[string]string) (*StorageRef, error) {
	return &StorageRef{}, nil
}
func (f *fakeStorage) StoreManifest(context.Context, string, map[string][]byte) error { return nil }
func (f *fakeStorage) GetSourcePath(context.Context, string) (string, error)          { return f.name, nil }
func (f *fakeStorage) Delete(context.Context, string) error                           { return nil }

// TestRouterSecretErrorLogsViaSlogNotStdout is the proof for #65: when per-user
// storage init fails (e.g. an unsupported backend), the router logs a generic
// warning through slog and falls back — it must NOT print the raw error, and
// the structured error detail must be at Debug, not Warn.
func TestRouterSecretErrorLogsViaSlogNotStdout(t *testing.T) {
	var buf bytes.Buffer
	// Warn-level handler: Debug records (which carry the error detail) are dropped.
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	fallback := &fakeStorage{name: "fallback"}
	keys := []config.APIKeyConf{
		{Name: "alice", Storage: &config.UserStorageConf{Backend: "bogus-backend"}},
	}
	r := NewUserStorageRouter(keys, fallback, t.TempDir(), logger)

	// Route as alice — createUserStorage returns an error for the bogus backend.
	ctx := vibedauth.WithUserID(context.Background(), "alice")
	got := r.resolve(ctx)

	if got != fallback {
		t.Errorf("expected fallback storage on init error, got %#v", got)
	}

	logged := buf.String()
	if !strings.Contains(logged, "per-user storage init failed") {
		t.Errorf("expected a Warn log line, got:\n%s", logged)
	}
	// The Warn line must NOT contain the raw error text (backend detail leak).
	if strings.Contains(logged, "unsupported per-user storage backend") {
		t.Errorf("raw error leaked into the Warn log:\n%s", logged)
	}
	// The user + backend fields are fine to log.
	if !strings.Contains(logged, "alice") {
		t.Errorf("expected user field in log, got:\n%s", logged)
	}
}

// TestRouterFallsBackWithoutUser confirms an unauthenticated context uses the
// fallback and never touches per-user config.
func TestRouterFallsBackWithoutUser(t *testing.T) {
	fallback := &fakeStorage{name: "fallback"}
	r := NewUserStorageRouter(nil, fallback, t.TempDir(), slog.Default())
	if got := r.resolve(context.Background()); got != fallback {
		t.Errorf("no-user context should resolve to fallback, got %#v", got)
	}
}
