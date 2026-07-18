package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// serviceScheme registers both VibedApp and core/v1 so the fake client can
// store the typed Service the manager creates alongside the unstructured
// SandboxClaim it reads.
func serviceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := vibedv1.AddToScheme(s); err != nil {
		t.Fatalf("vibed scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1 scheme: %v", err)
	}
	return s
}

// boundClaim builds an unstructured SandboxClaim whose status.sandbox.name is
// the adopted Sandbox name agent-sandbox reports after binding a pod.
func boundClaim(app *vibedv1.VibedApp, sandboxName string) *unstructured.Unstructured {
	c := newClaim()
	c.SetName(app.Name)
	c.SetNamespace(app.Namespace)
	_ = unstructured.SetNestedField(c.Object, sandboxName, "status", "sandbox", "name")
	return c
}

// TestEnsureServiceCreatesWithClaimSelector is the happy path: a bound claim
// yields a per-app Service whose selector mirrors the pod selector agent-sandbox
// publishes on the claim, with the stable cluster-DNS target returned to the
// caller.
func TestEnsureServiceCreatesWithClaimSelector(t *testing.T) {
	app := validApp("svc-app")
	claim := boundClaim(app, "static-nginx-n87dn")
	c := fake.NewClientBuilder().WithScheme(serviceScheme(t)).WithObjects(app, claim).Build()
	m := &K8sServiceManager{Client: c, AppPort: 8080}

	target, err := m.EnsureService(context.Background(), app)
	if err != nil {
		t.Fatalf("EnsureService: %v", err)
	}

	wantName := ServiceName(app)
	wantTarget := wantName + ".default.svc.cluster.local:8080"
	if target != wantTarget {
		t.Errorf("target = %q want %q", target, wantTarget)
	}

	var svc corev1.Service
	if err := c.Get(context.Background(), types.NamespacedName{Name: wantName, Namespace: "default"}, &svc); err != nil {
		t.Fatalf("get service: %v", err)
	}
	wantHash := sandboxNameHash("static-nginx-n87dn")
	if svc.Spec.Selector["agents.x-k8s.io/sandbox-name-hash"] != wantHash {
		t.Errorf("selector = %v want agents.x-k8s.io/sandbox-name-hash=%s", svc.Spec.Selector, wantHash)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 8080 {
		t.Errorf("ports = %v want a single :8080", svc.Spec.Ports)
	}
	if len(svc.OwnerReferences) != 1 || svc.OwnerReferences[0].Kind != "VibedApp" || svc.OwnerReferences[0].Name != app.Name {
		t.Errorf("owner refs = %v want one VibedApp ref", svc.OwnerReferences)
	}
}

// TestEnsureServiceIsIdempotent calls twice and expects exactly one Service
// and a stable target.
func TestEnsureServiceIsIdempotent(t *testing.T) {
	app := validApp("svc-idem")
	claim := boundClaim(app, "svc-idem-sandbox")
	c := fake.NewClientBuilder().WithScheme(serviceScheme(t)).WithObjects(app, claim).Build()
	m := &K8sServiceManager{Client: c, AppPort: 8080}

	var first string
	for i := 0; i < 3; i++ {
		got, err := m.EnsureService(context.Background(), app)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if i == 0 {
			first = got
		} else if got != first {
			t.Errorf("iteration %d: target=%q drifted from %q", i, got, first)
		}
	}

	var list corev1.ServiceList
	if err := c.List(context.Background(), &list, client.InNamespace("default")); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Errorf("expected exactly 1 Service, got %d", len(list.Items))
	}
}

// TestEnsureServiceReconcilesSelectorOnRebind covers a redeploy that rebinds
// to a new claim: the existing Service's selector must be repointed at the new
// claim UID so it keeps selecting the live pod.
func TestEnsureServiceReconcilesSelectorOnRebind(t *testing.T) {
	app := validApp("svc-rebind")
	claim := boundClaim(app, "new-sandbox")
	// A Service left over from a prior bind, still pointing at the old sandbox.
	stale := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ServiceName(app), Namespace: app.Namespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{"agents.x-k8s.io/sandbox-name-hash": sandboxNameHash("old-sandbox")},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 8080}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(serviceScheme(t)).WithObjects(app, claim, stale).Build()
	m := &K8sServiceManager{Client: c, AppPort: 8080}

	if _, err := m.EnsureService(context.Background(), app); err != nil {
		t.Fatalf("EnsureService: %v", err)
	}

	var svc corev1.Service
	if err := c.Get(context.Background(), types.NamespacedName{Name: ServiceName(app), Namespace: "default"}, &svc); err != nil {
		t.Fatalf("get service: %v", err)
	}
	wantHash := sandboxNameHash("new-sandbox")
	if svc.Spec.Selector["agents.x-k8s.io/sandbox-name-hash"] != wantHash {
		t.Errorf("selector = %v want repointed to sandbox-name-hash=%s", svc.Spec.Selector, wantHash)
	}
}

// TestEnsureServiceErrorsWithoutClaim: no SandboxClaim means no UID to select
// on, which is a real error (the app shouldn't reach this step unbound).
func TestEnsureServiceErrorsWithoutClaim(t *testing.T) {
	app := validApp("svc-noclaim")
	c := fake.NewClientBuilder().WithScheme(serviceScheme(t)).WithObjects(app).Build()
	m := &K8sServiceManager{Client: c, AppPort: 8080}

	if _, err := m.EnsureService(context.Background(), app); err == nil {
		t.Fatal("expected an error when the SandboxClaim is absent")
	}
}
