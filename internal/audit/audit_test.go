package audit

import (
	"context"
	"testing"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/store"
)

func TestRecorderPersistsWithActorFromContext(t *testing.T) {
	rec := New(store.NewMemoryStore(), nil)

	rec.Record(vibedauth.WithUserID(context.Background(), "alice"), "deploy", "app1", "ok", "")
	rec.Record(context.Background(), "delete", "app2", "error", "boom") // no actor in ctx

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

func TestNilRecorderIsNoop(t *testing.T) {
	var rec *Recorder
	rec.Record(context.Background(), "deploy", "x", "ok", "") // must not panic
	got, err := rec.List(context.Background(), store.AuditQuery{})
	if err != nil || got != nil {
		t.Fatalf("nil recorder List = %v, %v; want nil, nil", got, err)
	}
}

func TestRecorderWithoutStore(t *testing.T) {
	rec := New(nil, nil) // logs + counts only, no persistence
	rec.Record(context.Background(), "deploy", "x", "ok", "")
	got, err := rec.List(context.Background(), store.AuditQuery{})
	if err != nil || got != nil {
		t.Fatalf("storeless List = %v, %v; want nil, nil", got, err)
	}
}
