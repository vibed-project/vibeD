package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibed-project/vibeD/internal/audit"
	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
)

// quietLogger keeps the test output clean — auditHandler logs on the error
// path which we exercise deliberately.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func seedRecorder(t *testing.T) *audit.Recorder {
	t.Helper()
	rec := audit.New(store.NewMemoryStore(), quietLogger(), false)
	ctx := vibedauth.WithUserID(context.Background(), "alice")
	for _, e := range []struct{ action, target, outcome, detail string }{
		{"deploy", "app1", "ok", ""},
		{"deploy", "app2", "denied", "quota exceeded"},
		{"delete", "app1", "ok", ""},
	} {
		if err := rec.Record(ctx, e.action, e.target, e.outcome, e.detail); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return rec
}

// TestAuditHandlerRequiresAdmin locks in the auth gate: a request without
// the admin role gets 403, not the events list. This is the single line of
// defense against a non-admin reading the governance trail.
func TestAuditHandlerRequiresAdmin(t *testing.T) {
	h := auditHandler(seedRecorder(t), quietLogger())
	req := httptest.NewRequest(http.MethodGet, "/v1/audit", nil)
	req = req.WithContext(vibedauth.WithUserID(req.Context(), "alice")) // user, but not admin
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuditHandlerReturnsEventsForAdmin(t *testing.T) {
	h := auditHandler(seedRecorder(t), quietLogger())
	req := httptest.NewRequest(http.MethodGet, "/v1/audit", nil)
	req = req.WithContext(vibedauth.WithRole(vibedauth.WithUserID(req.Context(), "admin"), "admin"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp struct {
		Events []api.AuditEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Events) != 3 {
		t.Errorf("got %d events, want 3", len(resp.Events))
	}
}

// TestAuditHandlerFiltersByQuery exercises the actor/action/app/limit
// parameters — the surface CLI/admin clients rely on for incident triage.
func TestAuditHandlerFiltersByQuery(t *testing.T) {
	h := auditHandler(seedRecorder(t), quietLogger())

	cases := []struct {
		name      string
		query     string
		wantCount int
	}{
		{"by action", "?action=deploy", 2},
		{"by app", "?app=app1", 2},
		{"by actor", "?actor=alice", 3},
		{"by action+app", "?action=deploy&app=app1", 1},
		{"limit caps result", "?limit=1", 1},
		{"unknown actor returns empty", "?actor=ghost", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/audit"+tc.query, nil)
			req = req.WithContext(vibedauth.WithRole(vibedauth.WithUserID(req.Context(), "admin"), "admin"))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Events []api.AuditEvent `json:"events"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(resp.Events) != tc.wantCount {
				t.Errorf("got %d events, want %d (events=%+v)", len(resp.Events), tc.wantCount, resp.Events)
			}
		})
	}
}
