package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/pkg/api"
)

// auditStores returns each AuditStore implementation under a descriptive name.
func auditStores(t *testing.T) map[string]AuditStore {
	t.Helper()
	sq, err := NewSQLiteStore(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { sq.Close() })
	return map[string]AuditStore{
		"memory": NewMemoryStore(),
		"sqlite": sq,
	}
}

func TestAuditAppendListNewestFirstAndFilter(t *testing.T) {
	for name, s := range auditStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			base := time.Now().UTC()
			events := []api.AuditEvent{
				{ID: "1", Time: base.Add(1 * time.Second), Actor: "alice", Action: "deploy", Target: "app1", Outcome: "ok"},
				{ID: "2", Time: base.Add(2 * time.Second), Actor: "bob", Action: "delete", Target: "app2", Outcome: "ok"},
				{ID: "3", Time: base.Add(3 * time.Second), Actor: "alice", Action: "deploy", Target: "app3", Outcome: "denied"},
			}
			for i := range events {
				if err := s.AppendAudit(ctx, &events[i]); err != nil {
					t.Fatalf("append: %v", err)
				}
			}

			// Newest first.
			all, err := s.ListAudit(ctx, AuditQuery{})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(all) != 3 {
				t.Fatalf("got %d events, want 3", len(all))
			}
			if all[0].ID != "3" || all[2].ID != "1" {
				t.Fatalf("not newest-first: %v", []string{all[0].ID, all[1].ID, all[2].ID})
			}

			// Filter by actor.
			byAlice, _ := s.ListAudit(ctx, AuditQuery{Actor: "alice"})
			if len(byAlice) != 2 {
				t.Fatalf("alice events = %d, want 2", len(byAlice))
			}

			// Filter by action.
			deletes, _ := s.ListAudit(ctx, AuditQuery{Action: "delete"})
			if len(deletes) != 1 || deletes[0].Target != "app2" {
				t.Fatalf("delete filter = %+v, want 1 (app2)", deletes)
			}

			// Filter by target.
			t3, _ := s.ListAudit(ctx, AuditQuery{Target: "app3"})
			if len(t3) != 1 || t3[0].Outcome != "denied" {
				t.Fatalf("target filter = %+v", t3)
			}

			// Limit caps the result.
			limited, _ := s.ListAudit(ctx, AuditQuery{Limit: 1})
			if len(limited) != 1 || limited[0].ID != "3" {
				t.Fatalf("limit=1 = %+v, want [3]", limited)
			}
		})
	}
}

// TestAuditEnrichedFieldsRoundTrip verifies the optional enrichment fields
// survive an append/list cycle on every backend.
func TestAuditEnrichedFieldsRoundTrip(t *testing.T) {
	for name, s := range auditStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			want := api.AuditEvent{
				ID: "e1", Time: time.Now().UTC(), Actor: "alice", Action: "deploy", Target: "app1", Outcome: "ok",
				TenantID: "acme", SessionID: "sess-7", SourceHash: "deadbeef",
				PolicyDecision: "allowed", Before: "v1", After: "v2",
			}
			if err := s.AppendAudit(ctx, &want); err != nil {
				t.Fatalf("append: %v", err)
			}
			got, err := s.ListAudit(ctx, AuditQuery{})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d events, want 1", len(got))
			}
			g := got[0]
			if g.TenantID != want.TenantID || g.SessionID != want.SessionID ||
				g.SourceHash != want.SourceHash || g.PolicyDecision != want.PolicyDecision ||
				g.Before != want.Before || g.After != want.After {
				t.Fatalf("enriched fields not preserved:\n got  %+v\n want %+v", g, want)
			}
		})
	}
}
