package store

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/vibed-project/vibeD/pkg/api"

	"k8s.io/client-go/kubernetes/fake"
)

func cmTestArtifact(id, name string) *api.Artifact {
	return &api.Artifact{ID: id, Name: name, Status: api.StatusRunning, Target: api.TargetKubernetes}
}

// TestConfigMapStoreCRUD exercises the optimistic-concurrency write path against
// the fake clientset (no cluster needed).
func TestConfigMapStoreCRUD(t *testing.T) {
	s := NewConfigMapStore(fake.NewSimpleClientset(), "vibed-artifacts", "vibed")
	ctx := context.Background()

	if err := s.Create(ctx, cmTestArtifact("a1", "app-one")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Duplicate name is rejected.
	if err := s.Create(ctx, cmTestArtifact("a2", "app-one")); err == nil {
		t.Error("duplicate name should be rejected")
	} else if _, ok := err.(*api.ErrAlreadyExists); !ok {
		t.Errorf("want ErrAlreadyExists, got %T", err)
	}

	got, err := s.Get(ctx, "a1")
	if err != nil || got.Name != "app-one" {
		t.Fatalf("Get: %v (%+v)", err, got)
	}

	got.Status = api.StatusFailed
	if err := s.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if reread, _ := s.Get(ctx, "a1"); reread.Status != api.StatusFailed {
		t.Errorf("update not persisted: %v", reread.Status)
	}

	// Update of a missing artifact → ErrNotFound.
	if err := s.Update(ctx, cmTestArtifact("missing", "x")); err == nil {
		t.Error("update of missing artifact should fail")
	}

	if err := s.Delete(ctx, "a1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "a1"); err == nil {
		t.Error("artifact should be gone after delete")
	}
}

// TestConfigMapStoreSizeGuard is the proof for #71: a write that would push the
// ConfigMap Data past the byte ceiling is rejected with a clear error instead of
// silently attempting an over-limit object.
func TestConfigMapStoreSizeGuard(t *testing.T) {
	s := NewConfigMapStore(fake.NewSimpleClientset(), "vibed-artifacts", "vibed")
	ctx := context.Background()

	// A single artifact whose JSON is larger than the ceiling.
	big := cmTestArtifact("big", "huge")
	big.URL = strings.Repeat("x", maxConfigMapDataBytes+1)

	err := s.Create(ctx, big)
	if err == nil {
		t.Fatal("expected a size-limit error for an oversized artifact")
	}
	if !strings.Contains(err.Error(), "data limit") {
		t.Fatalf("expected a data-limit error, got %v", err)
	}
	// Nothing should have been persisted.
	if _, gerr := s.Get(ctx, "big"); gerr == nil {
		t.Error("oversized artifact must not be stored")
	}
}

// TestConfigMapStoreSequentialWritesPersist verifies the read-modify-write path
// preserves every prior entry (no whole-object clobber) across many writes.
//
// NOTE (#72): true anti-clobber under CONCURRENT writers relies on the API
// server returning a 409 Conflict on a stale ResourceVersion, which
// retry.RetryOnConflict then re-runs. The fake clientset does not enforce
// ResourceVersion conflicts, so it cannot exercise that retry — the concurrent
// guarantee is validated against a real cluster (see the integration tests).
// This test pins the mechanism that matters here: each write reads the latest
// object and merges, so entries accumulate rather than overwrite.
func TestConfigMapStoreSequentialWritesPersist(t *testing.T) {
	s := NewConfigMapStore(fake.NewSimpleClientset(), "vibed-artifacts", "vibed")
	ctx := context.Background()

	const n = 25
	for i := 0; i < n; i++ {
		id := "art-" + string(rune('A'+i%26)) + string(rune('0'+i/26))
		if err := s.Create(ctx, cmTestArtifact(id, id)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	list, err := s.List(ctx, ListOptions{AdminView: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list.Total != n {
		t.Errorf("after %d creates, store has %d artifacts (entries clobbered)", n, list.Total)
	}
}

// TestConfigMapStoreConcurrentCreatesDoNotError confirms the write path is
// safe to call concurrently — no panics, no deadlocks, and every call returns.
// (Exact final count under concurrency is a cluster-validated property; see the
// note on TestConfigMapStoreSequentialWritesPersist.)
func TestConfigMapStoreConcurrentCreatesDoNotError(t *testing.T) {
	s := NewConfigMapStore(fake.NewSimpleClientset(), "vibed-artifacts", "vibed")
	ctx := context.Background()

	const n = 25
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "art-" + string(rune('A'+i%26)) + string(rune('0'+i/26))
			errs[i] = s.Create(ctx, cmTestArtifact(id, id))
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Errorf("concurrent create %d errored: %v", i, e)
		}
	}
}
