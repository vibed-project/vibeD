package egressauthz

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

func TestK8sResolverAllowedFor(t *testing.T) {
	s := runtime.NewScheme()
	if err := vibedv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Egress: vibedv1.Egress{AllowedHosts: []string{"api.example.com"}}},
		Status:     vibedv1.VibedAppStatus{PodIP: "10.0.0.5"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(app).Build()
	r := &K8sResolver{Client: c, Namespace: "vibed-apps"}

	hosts, found := r.AllowedFor(context.Background(), "10.0.0.5")
	if !found || len(hosts) != 1 || hosts[0] != "api.example.com" {
		t.Fatalf("AllowedFor(bound IP) = %v, %v; want [api.example.com], true", hosts, found)
	}
	if _, found := r.AllowedFor(context.Background(), "10.9.9.9"); found {
		t.Error("unknown IP should not resolve to an app")
	}
	if _, found := r.AllowedFor(context.Background(), ""); found {
		t.Error("empty IP should not resolve")
	}
}
