package mcp

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

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
	cs := newSession(t, svc)

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
	cs := newSession(t, svc)

	callToolExpectError(t, cs, "get_artifact_logs", map[string]any{"artifact_id": "bobapp"})
}
