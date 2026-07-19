package mcp

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// historyRecords is a two-deploy history as the deploy service records it.
func historyRecords() []deploy.VersionRecord {
	return []deploy.VersionRecord{
		{Version: 1, Timestamp: "2026-07-01T10:00:00Z", Template: "python-flask", Lane: "python", SourceHash: "aaa", TarballKey: "app1.v1", TarballRef: "http://store.test/app1.v1.tar.gz"},
		{Version: 2, Timestamp: "2026-07-02T10:00:00Z", Template: "python-flask", Lane: "python", SourceHash: "bbb", TarballKey: "app1.v2", TarballRef: "http://store.test/app1.v2.tar.gz"},
	}
}

func TestListVersionsLive(t *testing.T) {
	svc := liveService(t, liveApp("app1", localOwner, "https://app1.vibed.test", vibedv1.PhaseReady, historyRecords()))
	cs := newSession(t, nil, svc) // nil orch: the live path must never touch it

	out := callTool(t, cs, "list_versions", map[string]any{"artifact_id": "app1"})
	assertKeys(t, out, "artifact_id", "versions")
	if out["artifact_id"] != "app1" {
		t.Errorf("artifact_id = %v, want app1", out["artifact_id"])
	}
	versions, ok := out["versions"].([]any)
	if !ok || len(versions) != 2 {
		t.Fatalf("versions = %#v, want 2 entries", out["versions"])
	}

	// Exact JSON keys: MCP clients depend on this shape staying stable across
	// the legacy and live backends.
	newest := versions[0].(map[string]any)
	assertKeys(t, newest, "version_id", "artifact_id", "version", "image_ref", "storage_ref", "status", "url", "created_at", "created_by")
	want := map[string]any{
		"version_id":  "app1.v2",
		"artifact_id": "app1",
		"version":     float64(2),
		"image_ref":   "", // warm-pool apps have no per-version image
		"storage_ref": "http://store.test/app1.v2.tar.gz",
		"status":      "running",
		"url":         "https://app1.vibed.test",
		"created_at":  "2026-07-02T10:00:00Z",
		"created_by":  localOwner,
	}
	for k, v := range want {
		if newest[k] != v {
			t.Errorf("versions[0][%q] = %v, want %v", k, newest[k], v)
		}
	}
	if oldest := versions[1].(map[string]any); oldest["version"] != float64(1) {
		t.Errorf("versions[1].version = %v, want 1 (newest-first ordering)", oldest["version"])
	}
}

func TestListVersionsLiveOwnership(t *testing.T) {
	// The MCP stdio identity is "local"; bob's app must be invisible.
	svc := liveService(t, liveApp("bobapp", "bob", "", vibedv1.PhaseReady, historyRecords()))
	cs := newSession(t, nil, svc)

	callToolExpectError(t, cs, "list_versions", map[string]any{"artifact_id": "bobapp"})
}

func TestListVersionsFallback(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	if err := st.Create(ctx, &api.Artifact{ID: "legacy1", Name: "legacy1", Status: api.StatusRunning, Target: api.TargetKubernetes}); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if err := st.CreateVersion(ctx, &api.ArtifactVersion{VersionID: "vid-1", ArtifactID: "legacy1", Version: 1, ImageRef: "registry.test/legacy1:v1", Status: api.StatusRunning, CreatedBy: "alice"}); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	cs := newSession(t, fallbackOrch(st, nil, deployer.NewFactory()), nil)

	out := callTool(t, cs, "list_versions", map[string]any{"artifact_id": "legacy1"})
	assertKeys(t, out, "artifact_id", "versions")
	versions, ok := out["versions"].([]any)
	if !ok || len(versions) != 1 {
		t.Fatalf("versions = %#v, want 1 entry", out["versions"])
	}
	if v := versions[0].(map[string]any); v["image_ref"] != "registry.test/legacy1:v1" {
		t.Errorf("fallback did not read the legacy ArtifactStore: image_ref = %v", v["image_ref"])
	}
}

func TestRollbackLive(t *testing.T) {
	svc := liveService(t, liveApp("app1", localOwner, "https://app1.vibed.test", vibedv1.PhaseReady, historyRecords()))
	cs := newSession(t, nil, svc)

	out := callTool(t, cs, "rollback_artifact", map[string]any{"artifact_id": "app1", "version": 1})
	assertKeys(t, out, "artifact_id", "name", "url", "target", "status", "image_ref", "new_version", "message")
	want := map[string]any{
		"artifact_id": "app1",
		"name":        "app1",
		"url":         "https://app1.vibed.test",
		"target":      "sandbox",
		"status":      "running",
		"image_ref":   "",
		"new_version": float64(3), // history had v1+v2, rollback appends v3
		"message":     "Successfully rolled back to version 1. New version is v3.",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%q = %v, want %v", k, out[k], v)
		}
	}

	// The CR must now point at the retained v1 source — that's what makes the
	// rollback effective for the controller.
	app := &vibedv1.VibedApp{}
	if err := svc.Client.Get(context.Background(), types.NamespacedName{Name: "app1", Namespace: "vibed-apps"}, app); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Spec.Source.TarballRef != "http://store.test/app1.v1.tar.gz" {
		t.Errorf("spec.source.tarballRef = %q, want the v1 source", app.Spec.Source.TarballRef)
	}
}

func TestRollbackLiveMissingVersion(t *testing.T) {
	svc := liveService(t, liveApp("app1", localOwner, "https://app1.vibed.test", vibedv1.PhaseReady, historyRecords()))
	cs := newSession(t, nil, svc)

	msg := callToolExpectError(t, cs, "rollback_artifact", map[string]any{"artifact_id": "app1", "version": 9})
	if !strings.Contains(msg, "not available for rollback") {
		t.Errorf("error = %q, want a version-not-available message", msg)
	}
}
