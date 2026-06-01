package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/templatevalidate"
)

func TestConfigMapTemplateGate(t *testing.T) {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(s).Build()

	// Seed the validation ConfigMap the gate reads: node-24 invalid, python-313 valid.
	v := &templatevalidate.Validator{Client: c, Namespace: "vibed-apps"}
	if err := v.Persist(context.Background(), []templatevalidate.Result{
		{Template: "node-24", Valid: false, Reason: "wrong language"},
		{Template: "python-313", Valid: true},
	}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	g := &ConfigMapTemplateGate{Client: c, Namespace: "vibed-apps"}

	if ok, reason := g.Allowed(context.Background(), "node-24"); ok || reason == "" {
		t.Errorf("invalid slot: allowed=%v reason=%q, want denied with a reason", ok, reason)
	}
	if ok, _ := g.Allowed(context.Background(), "python-313"); !ok {
		t.Error("valid slot should be allowed")
	}
	if ok, _ := g.Allowed(context.Background(), "never-validated"); !ok {
		t.Error("unvalidated slot should be allowed (warm-up window), not blocked")
	}
}

func TestConfigMapTemplateGateStrict(t *testing.T) {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	const validatedID = "docker-pullable://repo/image@sha256:aaaaaaaaaaaa"
	const driftedID = "docker-pullable://repo/image@sha256:bbbbbbbbbbbb"

	// Warm pod for python-313 still runs the validated image; a separate pod
	// for ts-22 has drifted (tag was re-pushed since validation).
	readyPod := func(slot, imageID string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      slot + "-pool-0",
				Namespace: "vibed-apps",
				Labels:    map[string]string{"vibed.dev/template": slot},
			},
			Status: corev1.PodStatus{
				Phase:  corev1.PodRunning,
				PodIP:  "10.0.0.1",
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
				},
				ContainerStatuses: []corev1.ContainerStatus{{ImageID: imageID}},
			},
		}
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(readyPod("python-313", validatedID), readyPod("ts-22", driftedID)).
		Build()
	v := &templatevalidate.Validator{Client: c, Namespace: "vibed-apps"}
	if err := v.Persist(context.Background(), []templatevalidate.Result{
		{Template: "node-24", Valid: false, Reason: "wrong language"},
		{Template: "python-313", Valid: true, ImageID: validatedID},
		{Template: "ts-22", Valid: true, ImageID: validatedID}, // pod now serves driftedID
		{Template: "missing-id", Valid: true, ImageID: ""},     // ancient result, no digest captured
	}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	g := &ConfigMapTemplateGate{Client: c, Namespace: "vibed-apps", Strict: true}

	cases := []struct {
		slot      string
		wantAllow bool
		reasonSub string
	}{
		{"node-24", false, "wrong language"},                                  // recorded failure: always denied
		{"python-313", true, ""},                                              // valid + ImageID matches live pod
		{"never-validated", false, "not yet validated"},                       // strict: warmup denial
		{"ts-22", false, "image has changed"},                                 // strict: digest drift
		{"missing-id", false, "no recorded ImageID"},                          // strict: legacy result
	}
	for _, tc := range cases {
		t.Run(tc.slot, func(t *testing.T) {
			ok, reason := g.Allowed(context.Background(), tc.slot)
			if ok != tc.wantAllow {
				t.Fatalf("allowed=%v, want %v (reason=%q)", ok, tc.wantAllow, reason)
			}
			if tc.reasonSub != "" && !strings.Contains(reason, tc.reasonSub) {
				t.Errorf("reason %q does not contain %q", reason, tc.reasonSub)
			}
		})
	}
}
