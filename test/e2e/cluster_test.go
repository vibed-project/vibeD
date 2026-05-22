//go:build e2ecluster

// cluster_test.go is the literal cluster E2E from refactor.md §10.2. Unlike
// slice_test.go (in-process, always runs), this needs a real cluster with the
// full vibeD stack already installed:
//
//	make dev                 # kind + agent-sandbox + deps
//	helm install vibed deploy/helm/vibed -n vibed-system --create-namespace \
//	     --set workerd.enabled=false
//	make e2e-cluster         # runs this file (-tags=e2ecluster)
//
// It is gated behind the e2ecluster build tag and skips when no cluster (or no
// VibedApp CRD) is reachable, so it never breaks the default `go test`.
package e2e

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/client/config"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

const clusterAppsNS = "vibed-apps"

// clusterClient builds a controller-runtime client from the ambient
// kubeconfig, or skips the test when none is available / the CRD is missing.
func clusterClient(t *testing.T) client.Client {
	t.Helper()
	cfg, err := ctrlcfg.GetConfig()
	if err != nil {
		t.Skipf("skipping cluster E2E: no kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := vibedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Skipf("skipping cluster E2E: cannot build client: %v", err)
	}
	// Probe the CRD: list VibedApps. A NoMatch/NotFound means the chart isn't
	// installed — skip rather than fail.
	var list vibedv1.VibedAppList
	if err := c.List(context.Background(), &list); err != nil {
		t.Skipf("skipping cluster E2E: VibedApp CRD not reachable (chart installed?): %v", err)
	}
	return c
}

// TestClusterDeployReachesReady creates a VibedApp directly and waits for the
// controller to drive it to Ready with a URL. It assumes the source tarball
// referenced already exists (or that the agent tolerates a static template);
// for a fuller test, point sourceRef at a real uploaded tarball.
//
// NOTE: this is intentionally a thin smoke test. The richer "POST /v1/deploy,
// curl the returned URL, p99 < 10s over 100 deploys" loop from §10.2 belongs
// in a load harness (test/load, milestone F7-load) once images are published.
func TestClusterDeployReachesReady(t *testing.T) {
	c := clusterClient(t)
	ctx := context.Background()

	name := "e2e-static"
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: clusterAppsNS},
		Spec: vibedv1.VibedAppSpec{
			Owner:  "e2e@vibed.test",
			Source: vibedv1.Source{TarballRef: "http://example.invalid/placeholder.tar.gz"},
			Runtime: vibedv1.Runtime{
				Lane:     vibedv1.LaneFast,
				Template: "static-nginx",
			},
		},
	}
	_ = c.Delete(ctx, app) // clean any prior run
	if err := c.Create(ctx, app); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create VibedApp: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), app) })

	deadline := time.Now().Add(60 * time.Second)
	for {
		got := &vibedv1.VibedApp{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: clusterAppsNS}, got); err != nil {
			t.Fatalf("get VibedApp: %v", err)
		}
		switch got.Status.Phase {
		case vibedv1.PhaseReady:
			if got.Status.URL == "" {
				t.Fatal("Ready but no URL")
			}
			t.Logf("app Ready at %s", got.Status.URL)
			return
		case vibedv1.PhaseFailed:
			t.Fatalf("app Failed: %+v", got.Status.Conditions)
		}
		if time.Now().After(deadline) {
			t.Fatalf("app never reached Ready (last phase %q)", got.Status.Phase)
		}
		time.Sleep(time.Second)
	}
}
