package mcp

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// sandboxPodLabel mirrors internal/deploy's label for agent-sandbox pods; the
// fake pod must carry it for the live log path to find the bound pod.
const sandboxPodLabel = "agents.x-k8s.io/sandbox-name-hash"

func TestGetArtifactLogsLive(t *testing.T) {
	app := liveApp("app1", localOwner, "https://app1.vibed.test", vibedv1.PhaseReady, nil)
	app.Status.PodIP = "10.0.0.5"
	svc := liveService(t, app)
	svc.Clientset = k8sfake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app1-pod", Namespace: "vibed-apps",
			Labels: map[string]string{sandboxPodLabel: "h1"},
		},
		Status: corev1.PodStatus{PodIP: "10.0.0.5"},
	})
	cs := newSession(t, nil, svc) // nil orch: the live path must never touch it

	out := callTool(t, cs, "get_artifact_logs", map[string]any{"artifact_id": "app1", "lines": 10})
	assertKeys(t, out, "logs")
	// client-go's fake log stream always yields this fixed body.
	if out["logs"] != "fake logs" {
		t.Errorf("logs = %q, want %q", out["logs"], "fake logs")
	}
}

func TestGetArtifactLogsLiveOwnership(t *testing.T) {
	app := liveApp("bobapp", "bob", "", vibedv1.PhaseReady, nil)
	app.Status.PodIP = "10.0.0.5"
	svc := liveService(t, app)
	svc.Clientset = k8sfake.NewSimpleClientset()
	cs := newSession(t, nil, svc)

	callToolExpectError(t, cs, "get_artifact_logs", map[string]any{"artifact_id": "bobapp"})
}

func TestGetArtifactLogsFallback(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	if err := st.Create(ctx, &api.Artifact{ID: "legacy1", Name: "legacy1", Status: api.StatusRunning, Target: api.TargetSandbox}); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	f := deployer.NewFactory()
	f.Register(api.TargetSandbox, &fakeDeployer{logs: []string{"line-1", "line-2"}})
	cs := newSession(t, fallbackOrch(st, nil, f), nil)

	out := callTool(t, cs, "get_artifact_logs", map[string]any{"artifact_id": "legacy1"})
	assertKeys(t, out, "logs")
	if out["logs"] != "line-1\nline-2" {
		t.Errorf("logs = %q, want the legacy deployer's lines", out["logs"])
	}
}
