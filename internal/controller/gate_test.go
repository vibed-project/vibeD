package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
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
