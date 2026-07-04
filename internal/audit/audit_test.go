package audit

import (
	"context"
	"errors"
	"testing"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
)

func TestRecorderPersistsWithActorFromContext(t *testing.T) {
	rec := New(store.NewMemoryStore(), nil, false)

	if err := rec.Record(vibedauth.WithUserID(context.Background(), "alice"), "deploy", "app1", "ok", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := rec.Record(context.Background(), "delete", "app2", "error", "boom"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	events, err := rec.List(context.Background(), store.AuditQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	// Newest first.
	if events[0].Action != "delete" || events[0].Actor != "" || events[0].Detail != "boom" {
		t.Errorf("event[0] = %+v", events[0])
	}
	if events[1].Action != "deploy" || events[1].Actor != "alice" || events[1].Outcome != "ok" {
		t.Errorf("event[1] = %+v", events[1])
	}
	if events[1].ID == "" || events[1].Time.IsZero() {
		t.Errorf("expected ID + Time populated, got %+v", events[1])
	}
}

func TestRecordMergesContextFields(t *testing.T) {
	rec := New(store.NewMemoryStore(), nil, false)

	ctx := WithFields(vibedauth.WithUserID(context.Background(), "alice"),
		Fields{TenantID: "acme", SourceHash: "cafebabe", PolicyDecision: "allowed"})
	if err := rec.Record(ctx, "deploy", "app1", "ok", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// A record under a bare context leaves the enrichment empty.
	if err := rec.Record(context.Background(), "delete", "app1", "ok", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	events, err := rec.List(context.Background(), store.AuditQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	// Newest first: the un-enriched delete.
	if events[0].TenantID != "" || events[0].SourceHash != "" || events[0].PolicyDecision != "" {
		t.Errorf("un-enriched event carried fields: %+v", events[0])
	}
	// The enriched deploy.
	if events[1].TenantID != "acme" || events[1].SourceHash != "cafebabe" || events[1].PolicyDecision != "allowed" {
		t.Errorf("enriched fields not merged: %+v", events[1])
	}
}

func TestNilRecorderIsNoop(t *testing.T) {
	var rec *Recorder
	if err := rec.Record(context.Background(), "deploy", "x", "ok", ""); err != nil { // must not panic
		t.Fatalf("nil recorder Record: %v", err)
	}
	got, err := rec.List(context.Background(), store.AuditQuery{})
	if err != nil || got != nil {
		t.Fatalf("nil recorder List = %v, %v; want nil, nil", got, err)
	}
}

func TestRecorderWithoutStore(t *testing.T) {
	rec := New(nil, nil, true) // logs + counts only, no persistence
	if err := rec.Record(context.Background(), "deploy", "x", "ok", ""); err != nil {
		t.Fatalf("storeless Record: %v; want nil (no store ⇒ no persist failure)", err)
	}
	got, err := rec.List(context.Background(), store.AuditQuery{})
	if err != nil || got != nil {
		t.Fatalf("storeless List = %v, %v; want nil, nil", got, err)
	}
}

// failingStore returns the configured error on every AppendAudit.
type failingStore struct{ err error }

func (f failingStore) AppendAudit(context.Context, *api.AuditEvent) error { return f.err }
func (f failingStore) ListAudit(context.Context, store.AuditQuery) ([]api.AuditEvent, error) {
	return nil, nil
}

func TestRecorderFailClosed(t *testing.T) {
	boom := errors.New("disk full")
	rec := New(failingStore{err: boom}, nil, true)

	err := rec.Record(context.Background(), "deploy", "x", "ok", "")
	if err == nil {
		t.Fatal("expected error from fail-closed Recorder when store fails")
	}
	if !errors.Is(err, boom) {
		t.Errorf("expected wrapped %v, got %v", boom, err)
	}
}

func TestRecorderFailOpen(t *testing.T) {
	boom := errors.New("disk full")
	rec := New(failingStore{err: boom}, nil, false)

	if err := rec.Record(context.Background(), "deploy", "x", "ok", ""); err != nil {
		t.Errorf("fail-open Recorder must swallow store error, got %v", err)
	}
}
