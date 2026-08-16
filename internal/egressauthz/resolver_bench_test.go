package egressauthz

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// BenchmarkAllowedFor measures the per-request pod-IP → allow-list lookup over
// a 500-app namespace — the hot path hit for every outbound sandbox connection.
func BenchmarkAllowedFor(b *testing.B) {
	s := runtime.NewScheme()
	if err := vibedv1.AddToScheme(s); err != nil {
		b.Fatalf("scheme: %v", err)
	}
	objs := make([]client.Object, 0, 500)
	for i := 0; i < 500; i++ {
		objs = append(objs, &vibedv1.VibedApp{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("app-%03d", i), Namespace: "vibed-apps"},
			Spec:       vibedv1.VibedAppSpec{Egress: vibedv1.Egress{AllowedHosts: []string{fmt.Sprintf("api-%03d.example.com", i)}}},
			Status:     vibedv1.VibedAppStatus{PodIP: fmt.Sprintf("10.0.%d.%d", i/250, i%250+1)},
		})
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithIndex(&vibedv1.VibedApp{}, PodIPIndexField, IndexVibedAppPodIP).
		WithObjects(objs...).
		Build()
	r := &K8sResolver{Client: c, Namespace: "vibed-apps"}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, found := r.AllowedFor(ctx, "10.0.1.250"); !found {
			b.Fatal("expected a match for the seeded pod IP")
		}
	}
}
