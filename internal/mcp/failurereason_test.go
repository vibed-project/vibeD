package mcp

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

func failedApp(reason, message string) *vibedv1.VibedApp {
	app := &vibedv1.VibedApp{}
	app.Name = "js-hello"
	app.Status.Phase = vibedv1.PhaseFailed
	app.Status.Conditions = []metav1.Condition{{
		Type:    vibedv1.ConditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	}}
	return app
}

// An agent that gets a bare "failed" with no explanation goes looking for logs,
// finds none (the app never got a pod), and wrongly retries a permanent
// failure. get_artifact_status must carry the reason instead.
func TestAppToArtifact_CarriesFailureMessage(t *testing.T) {
	const msg = `no SandboxTemplate for template "node-24" (no warm pool configured for it)`
	got := appToArtifact(failedApp("TemplateMissing", msg))

	if got.Error != msg {
		t.Errorf("Error = %q, want %q", got.Error, msg)
	}
}

// A healthy app must not carry an error, even though its Ready condition may
// still have a reason set.
func TestAppToArtifact_ReadyAppHasNoError(t *testing.T) {
	app := failedApp("Serving", "app is serving")
	app.Status.Phase = vibedv1.PhaseReady
	app.Status.Conditions[0].Status = metav1.ConditionTrue

	if got := appToArtifact(app); got.Error != "" {
		t.Errorf("Error = %q, want empty for a Ready app", got.Error)
	}
}

// No conditions (fresh app) must not panic or invent an error.
func TestAppToArtifact_NoConditions(t *testing.T) {
	app := &vibedv1.VibedApp{}
	app.Name = "fresh"
	app.Status.Phase = vibedv1.PhasePending

	if got := appToArtifact(app); got.Error != "" {
		t.Errorf("Error = %q, want empty", got.Error)
	}
}
