package vibedhttp

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// A deploy that fails before any pod exists (no warm pool for the required
// template) produces no logs, so the Ready condition is the only diagnosis.
// toAPIApp must surface it or clients can only report "no detail available".
func TestToAPIApp_SurfacesFailureReason(t *testing.T) {
	app := &vibedv1.VibedApp{}
	app.Name = "js-hello"
	app.Status.Phase = vibedv1.PhaseFailed
	app.Status.Conditions = []metav1.Condition{
		{Type: vibedv1.ConditionSourceValid, Status: metav1.ConditionTrue, Reason: "Valid"},
		{
			Type:    vibedv1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  "TemplateMissing",
			Message: `no SandboxTemplate for template "node-24" (no warm pool configured for it)`,
		},
	}

	out := toAPIApp(app)

	if out.Reason == nil || *out.Reason != "TemplateMissing" {
		t.Errorf("Reason = %v, want TemplateMissing", derefStr(out.Reason))
	}
	if out.Message == nil || *out.Message != `no SandboxTemplate for template "node-24" (no warm pool configured for it)` {
		t.Errorf("Message = %q, want the TemplateMissing explanation", derefStr(out.Message))
	}
}

// A healthy app carries the Ready condition's reason too, and never a stale
// message from another condition type.
func TestToAPIApp_ReadyAppReason(t *testing.T) {
	app := &vibedv1.VibedApp{}
	app.Name = "hello"
	app.Status.Phase = vibedv1.PhaseReady
	app.Status.URL = "http://abc.localhost"
	app.Status.Conditions = []metav1.Condition{
		{Type: vibedv1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Serving"},
	}

	out := toAPIApp(app)

	if out.Reason == nil || *out.Reason != "Serving" {
		t.Errorf("Reason = %v, want Serving", derefStr(out.Reason))
	}
	if out.Message != nil {
		t.Errorf("Message = %q, want nil (condition has no message)", derefStr(out.Message))
	}
}

// No conditions at all (e.g. a freshly created app) must not panic or invent
// fields.
func TestToAPIApp_NoConditions(t *testing.T) {
	app := &vibedv1.VibedApp{}
	app.Name = "fresh"
	app.Status.Phase = vibedv1.PhasePending

	out := toAPIApp(app)

	if out.Reason != nil || out.Message != nil {
		t.Errorf("Reason/Message = %q/%q, want both nil", derefStr(out.Reason), derefStr(out.Message))
	}
}

func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
