package v1alpha1

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestVibedAppRoundTrip marshals a fully-populated VibedApp to JSON, decodes
// it, re-marshals, and asserts the JSON bytes are stable. Catches missing or
// misspelled json tags — the most common bug for hand-written CRD types. We
// compare JSON instead of structs so timezone normalization on time.Time
// fields doesn't generate false failures.
func TestVibedAppRoundTrip(t *testing.T) {
	original := newPopulatedApp()

	firstPass, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("first marshal: %v", err)
	}

	var decoded VibedApp
	if err := json.Unmarshal(firstPass, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	secondPass, err := json.Marshal(&decoded)
	if err != nil {
		t.Fatalf("second marshal: %v", err)
	}

	if string(firstPass) != string(secondPass) {
		t.Fatalf("round-trip JSON mismatch:\nfirst =%s\nsecond=%s", firstPass, secondPass)
	}
}

// TestVibedAppDeepCopy ensures the generated DeepCopy actually deep-copies:
// mutating the copy must not affect the original. Cheap insurance against the
// generator silently omitting a slice/map field.
func TestVibedAppDeepCopy(t *testing.T) {
	original := newPopulatedApp()
	cp := original.DeepCopy()

	if !reflect.DeepEqual(original, cp) {
		t.Fatalf("DeepCopy not equal to original")
	}

	// Mutate every collection in the copy and ensure the original is intact.
	cp.Spec.Runtime.Env[0].Value = "mutated"
	cp.Status.Conditions[0].Message = "mutated"
	cp.Status.SnapshotRef = "mutated"

	if original.Spec.Runtime.Env[0].Value == "mutated" {
		t.Errorf("DeepCopy did not isolate Spec.Runtime.Env")
	}
	if original.Status.Conditions[0].Message == "mutated" {
		t.Errorf("DeepCopy did not isolate Status.Conditions")
	}
	if original.Status.SnapshotRef == "mutated" {
		t.Errorf("DeepCopy did not isolate Status.SnapshotRef")
	}
}

func newPopulatedApp() *VibedApp {
	deployedAt := metav1.NewTime(time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC))
	return &VibedApp{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupVersion.String(),
			Kind:       "VibedApp",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello-world",
			Namespace: "vibed-apps",
		},
		Spec: VibedAppSpec{
			Owner: "alice@example.com",
			Source: Source{
				TarballRef: "s3://vibed-sources/hello-world.tar.gz",
			},
			Runtime: Runtime{
				Lane:       LaneGeneral,
				Template:   "node-24",
				Entrypoint: "npm start",
				Env: []EnvVar{
					{Name: "DEBUG", Value: "1"},
					{Name: "API_BASE", Value: "https://api.example.com"},
				},
			},
			Resources: Resources{
				CPU:    "500m",
				Memory: "256Mi",
			},
			TTL: "30m",
		},
		Status: VibedAppStatus{
			Phase:          PhaseReady,
			URL:            "https://abc123def456.vibed.example.com",
			SandboxRef:     "sandbox-xyz",
			LastDeployedAt: &deployedAt,
			SnapshotRef:    "s3://vibed-snapshots/hello-world/0001.snap",
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "SandboxRunning",
					Message:            "user process listening on :8080",
					LastTransitionTime: deployedAt,
				},
			},
		},
	}
}
