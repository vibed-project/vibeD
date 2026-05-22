package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// newFakeClient builds a fake controller-runtime client that knows the
// VibedApp scheme plus enough about the unstructured SandboxClaim GVK to
// store and retrieve claims.
func newFakeClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	s := newScheme(t)
	return fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objects...).
		WithStatusSubresource(&vibedv1.VibedApp{}).
		Build()
}

func newSandboxClaimer(c client.Client) *SandboxClaimer {
	return &SandboxClaimer{Client: c, PoolNamespace: "vibed-pools"}
}

// TestEnsureClaimCreatesWhenMissing verifies the first EnsureClaim call
// creates the SandboxClaim and returns not-bound (the new claim has no
// status yet).
func TestEnsureClaimCreatesWhenMissing(t *testing.T) {
	app := validApp("create-me")
	c := newFakeClient(t, app)
	cl := newSandboxClaimer(c)

	bound, ref, ip, err := cl.EnsureClaim(context.Background(), app)
	if err != nil {
		t.Fatalf("EnsureClaim: %v", err)
	}
	if bound {
		t.Errorf("bound should be false for a freshly created claim")
	}
	if ref != "" || ip != "" {
		t.Errorf("bound=false case must return empty ref/ip, got ref=%q ip=%q", ref, ip)
	}

	// Verify the claim actually got written.
	got := newClaim()
	if err := c.Get(context.Background(), types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, got); err != nil {
		t.Fatalf("get created claim: %v", err)
	}
	if k := got.GetKind(); k != "SandboxClaim" {
		t.Errorf("created kind=%q want SandboxClaim", k)
	}
	if labels := got.GetLabels(); labels["vibed.dev/template"] != "node-24" {
		t.Errorf("template label missing/wrong: %v", labels)
	}
	tplName, _, _ := unstructured.NestedString(got.Object, "spec", "sandboxTemplateRef", "name")
	if tplName != "node-24" {
		t.Errorf("spec.sandboxTemplateRef.name = %q want node-24", tplName)
	}
	wp, _, _ := unstructured.NestedString(got.Object, "spec", "warmpool")
	if wp != "node-24" {
		t.Errorf("spec.warmpool = %q want node-24", wp)
	}
}

// TestEnsureClaimIsIdempotent makes sure a second EnsureClaim doesn't
// create a duplicate and doesn't fall over when the claim already exists.
func TestEnsureClaimIsIdempotent(t *testing.T) {
	app := validApp("idempotent")
	c := newFakeClient(t, app)
	cl := newSandboxClaimer(c)

	for i := 0; i < 3; i++ {
		bound, _, _, err := cl.EnsureClaim(context.Background(), app)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if bound {
			t.Errorf("iteration %d: claim is still unbound, bound should be false", i)
		}
	}
	// Exactly one claim in the namespace.
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(SandboxClaimGVK)
	if err := c.List(context.Background(), list, client.InNamespace(app.Namespace)); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Errorf("expected exactly 1 SandboxClaim, got %d", len(list.Items))
	}
}

// TestEnsureClaimReportsBoundOnceStatusPopulated verifies the read path:
// once agent-sandbox writes .status.sandbox.{name,podIPs[0]}, the claimer
// surfaces (bound=true, sandboxRef, podIP).
func TestEnsureClaimReportsBoundOnceStatusPopulated(t *testing.T) {
	app := validApp("bound-app")
	// Pre-create a claim with a populated status, simulating agent-sandbox
	// having bound a pod. ResourceVersion is required by the fake client.
	preBound := &unstructured.Unstructured{}
	preBound.SetGroupVersionKind(SandboxClaimGVK)
	preBound.SetName(app.Name)
	preBound.SetNamespace(app.Namespace)
	preBound.SetOwnerReferences([]metav1.OwnerReference{
		{APIVersion: vibedv1.GroupVersion.String(), Kind: "VibedApp", Name: app.Name, UID: app.UID},
	})
	_ = unstructured.SetNestedMap(preBound.Object, map[string]any{
		"sandboxTemplateRef": map[string]any{"name": "node-24"},
		"warmpool":           "node-24",
	}, "spec")
	_ = unstructured.SetNestedMap(preBound.Object, map[string]any{
		"sandbox": map[string]any{
			"name":   "sb-abc123",
			"podIPs": []any{"10.0.0.42", "fd00::1"},
		},
	}, "status")

	c := newFakeClient(t, app, preBound)
	cl := newSandboxClaimer(c)

	bound, ref, ip, err := cl.EnsureClaim(context.Background(), app)
	if err != nil {
		t.Fatalf("EnsureClaim: %v", err)
	}
	if !bound {
		t.Errorf("bound should be true once status.sandbox.{name,podIPs[0]} is populated")
	}
	if ref != "sb-abc123" {
		t.Errorf("sandboxRef = %q want sb-abc123", ref)
	}
	if ip != "10.0.0.42" {
		t.Errorf("podIP = %q want 10.0.0.42 (first podIP)", ip)
	}
}

// TestEnsureClaimRequiresTemplate refuses to claim if the VibedApp doesn't
// specify a template — the classifier should have set one, and a missing
// value is a programmer error worth surfacing as an error rather than
// silently picking a default.
func TestEnsureClaimRequiresTemplate(t *testing.T) {
	app := validApp("no-template")
	app.Spec.Runtime.Template = ""
	c := newFakeClient(t, app)
	cl := newSandboxClaimer(c)

	_, _, _, err := cl.EnsureClaim(context.Background(), app)
	if err == nil {
		t.Fatal("EnsureClaim with empty template should error")
	}
}
