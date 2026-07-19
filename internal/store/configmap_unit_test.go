package store

import (
	"context"
	"strings"
	"testing"

	"github.com/vibed-project/vibeD/pkg/api"
	"k8s.io/client-go/kubernetes/fake"
)

func newFakeConfigMapStore() *ConfigMapStore {
	return NewConfigMapStore(fake.NewSimpleClientset(), "vibed-artifacts", "vibed-system")
}

// TestConfigMapStore_CRUD exercises the lock-free reads + retry-wrapped writes
// (#72) end to end against the fake clientset: the round-trips must still behave.
func TestConfigMapStore_CRUD(t *testing.T) {
	ctx := context.Background()
	s := newFakeConfigMapStore()

	a := &api.Artifact{ID: "a1", Name: "app-one", OwnerID: "alice", Status: api.StatusRunning}
	if err := s.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Duplicate name is rejected (not a conflict → not retried).
	if err := s.Create(ctx, &api.Artifact{ID: "a2", Name: "app-one"}); err == nil {
		t.Error("Create with duplicate name should fail")
	}

	got, err := s.Get(ctx, "a1")
	if err != nil || got.Name != "app-one" {
		t.Fatalf("Get: %v, %+v", err, got)
	}
	if byName, err := s.GetByName(ctx, "app-one"); err != nil || byName.ID != "a1" {
		t.Fatalf("GetByName: %v, %+v", err, byName)
	}

	a.Status = api.StatusFailed
	if err := s.Update(ctx, a); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ := s.Get(ctx, "a1"); got.Status != api.StatusFailed {
		t.Errorf("after Update status = %q, want failed", got.Status)
	}

	list, err := s.List(ctx, ListOptions{AdminView: true})
	if err != nil || list.Total != 1 {
		t.Fatalf("List: %v, total=%d", err, list.Total)
	}

	if err := s.Delete(ctx, "a1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "a1"); err == nil {
		t.Error("Get after Delete should fail")
	}
	// Deleting a missing artifact is ErrNotFound, not a retry storm.
	if err := s.Delete(ctx, "missing"); err == nil {
		t.Error("Delete of missing artifact should return ErrNotFound")
	}
}

// TestConfigMapStore_SizeGuard locks in #71: a write that would push the store
// ConfigMap past the etcd safety limit is rejected with an actionable error
// before hitting the API, instead of bricking on a cryptic "entity too large".
func TestConfigMapStore_SizeGuard(t *testing.T) {
	ctx := context.Background()
	s := newFakeConfigMapStore()

	// One artifact whose serialized JSON alone exceeds the limit. StaticFiles is
	// a persisted string field (EnvVars/SecretRefs are json:"-" in this store).
	big := &api.Artifact{
		ID:          "big",
		Name:        "big-app",
		StaticFiles: strings.Repeat("x", maxConfigMapBytes+1),
	}
	err := s.Create(ctx, big)
	if err == nil {
		t.Fatal("Create of an oversized artifact should be rejected by the size guard")
	}
	if !strings.Contains(err.Error(), "safety limit") {
		t.Errorf("error = %q, want the size-limit guard message", err)
	}
	// The store is not bricked: a normal artifact still writes fine.
	if err := s.Create(ctx, &api.Artifact{ID: "ok", Name: "ok-app"}); err != nil {
		t.Errorf("normal Create after a rejected oversized write should succeed: %v", err)
	}
}
