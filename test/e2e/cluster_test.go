//go:build e2ecluster

// cluster_test.go is the literal cluster E2E from refactor.md §10.2. It drives
// real deploys through a running stack and also exercises bring-your-own
// base-image validation (a mis-imaged slot must be gated). It needs a cluster
// with the full vibeD stack installed and the server reachable (set
// VIBED_E2E_URL, default http://localhost:18090):
//
//	# CI runs this via .github/workflows/e2e-cluster.yaml. Locally:
//	helm install vibed deploy/helm/vibed -n vibed-system --create-namespace \
//	     -f deploy/helm/vibed/values-kind.yaml --wait
//	kubectl port-forward -n vibed-system svc/vibed 18090:8080 &
//	make e2e-cluster
//
// Gated behind the e2ecluster build tag, and skips when the server isn't
// reachable, so the default `go test` is unaffected.
package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/vibed-project/vibeD/internal/templatevalidate"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

const appsNamespace = "vibed-apps"

// baseURL is the vibed server under test; the workflow sets VIBED_E2E_URL.
func baseURL() string {
	if u := os.Getenv("VIBED_E2E_URL"); u != "" {
		return u
	}
	return "http://localhost:18090"
}

// requireServer skips unless the vibed server answers /healthz.
func requireServer(t *testing.T) string {
	t.Helper()
	base := baseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("skipping cluster E2E: vibed not reachable at %s: %v", base, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("skipping cluster E2E: %s/healthz returned %d", base, resp.StatusCode)
	}
	return base
}

// k8sClient builds a controller-runtime client (corev1 + VibedApp) from the
// ambient kubeconfig, skipping when none is available.
func k8sClient(t *testing.T) client.Client {
	t.Helper()
	cfg, err := ctrlcfg.GetConfig()
	if err != nil {
		t.Skipf("skipping: no kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	if err := vibedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Skipf("skipping: cannot build client: %v", err)
	}
	return c
}

// buildTarball builds a gzipped tar from path→content.
func buildTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

type deployResp struct {
	AppID     string `json:"app_id"`
	URL       string `json:"url"`
	StatusURL string `json:"status_url"`
}

// deployApp POSTs a multipart deploy and returns the app id.
func deployApp(t *testing.T, base, name string, files map[string]string) string {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	src, _ := mw.CreateFormFile("source", "source.tar.gz")
	if _, err := src.Write(buildTarball(t, files)); err != nil {
		t.Fatalf("write source: %v", err)
	}
	meta, _ := mw.CreateFormField("metadata")
	meta.Write([]byte(`{"name":"` + name + `"}`))
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/deploy", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /v1/deploy: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// 200 = Ready; 202 = accepted (poll); anything else (e.g. the app fails the
	// gate fast) we still inspect the CR below, so don't fail hard here unless
	// the request itself was rejected.
	if resp.StatusCode >= 500 {
		t.Fatalf("deploy returned %d: %s", resp.StatusCode, raw)
	}
	var dr deployResp
	_ = json.Unmarshal(raw, &dr)
	if dr.AppID == "" {
		dr.AppID = name
	}
	return dr.AppID
}

// waitPhase polls the VibedApp CR until its phase is one of want (or timeout),
// returning the final app.
func waitPhase(t *testing.T, c client.Client, name string, timeout time.Duration, want ...vibedv1.Phase) *vibedv1.VibedApp {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	app := &vibedv1.VibedApp{}
	for {
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: appsNamespace}, app); err == nil {
			for _, w := range want {
				if app.Status.Phase == w {
					return app
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("app %s never reached %v (last phase %q)", name, want, app.Status.Phase)
		}
		time.Sleep(2 * time.Second)
	}
}

// waitTemplateValid blocks until the BYO validator records slot as valid. The
// validator's first pass runs at controller start — before the warm-pool pods
// are Ready — and marks every slot invalid ("no Ready warm-pool pod"); a later
// pass (every 2m) flips a healthy slot to valid once its pod is up. A
// happy-path deploy must wait for that, or the claim hard-gate fails it.
// Mirrors the invalid-poll in TestClusterInvalidImageIsGated.
func waitTemplateValid(t *testing.T, c client.Client, slot string, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for {
		r, found, err := templatevalidate.LoadResult(ctx, c, appsNamespace, slot)
		if err == nil && found && r.Valid {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("template %q never validated valid within %s (found=%v)", slot, timeout, found)
		}
		time.Sleep(3 * time.Second)
	}
}

// TestClusterDeployReachesReady is the happy path: a static site deploys to a
// valid slot and reaches Ready with a URL.
func TestClusterDeployReachesReady(t *testing.T) {
	base := requireServer(t)
	const name = "e2e-static"
	c := k8sClient(t)

	// Don't race the validator's warm-up pass: wait for the static-nginx slot
	// to be marked valid before deploying, else the claim gate fails the app.
	waitTemplateValid(t, c, "static-nginx", 3*time.Minute)

	deployApp(t, base, name, map[string]string{"index.html": "<!doctype html><h1>cluster e2e</h1>"})
	t.Cleanup(func() { deleteApp(base, name) })

	app := waitPhase(t, c, name, 120*time.Second, vibedv1.PhaseReady, vibedv1.PhaseFailed)
	if app.Status.Phase != vibedv1.PhaseReady {
		t.Fatalf("static app phase=%q, want Ready: %+v", app.Status.Phase, app.Status.Conditions)
	}
	if app.Status.URL == "" {
		t.Fatal("Ready but no URL")
	}
	t.Logf("static app Ready at %s", app.Status.URL)
}

// TestClusterInvalidImageIsGated exercises bring-your-own validation: the e2e
// install points the node-24 slot at the static-nginx image (declares language
// "static", not "node"). The validator must record node-24 invalid, and a Node
// deploy (which the classifier routes to node-24) must be hard-gated to Failed
// with TemplateInvalid rather than running on the wrong-language image.
func TestClusterInvalidImageIsGated(t *testing.T) {
	base := requireServer(t)
	c := k8sClient(t)
	ctx := context.Background()
	const slot = "node-24"

	// Wait for the validator to mark the mis-imaged slot invalid.
	deadline := time.Now().Add(3 * time.Minute)
	var res templatevalidate.Result
	for {
		r, found, err := templatevalidate.LoadResult(ctx, c, appsNamespace, slot)
		if err == nil && found && !r.Valid {
			res = r
			break
		} else if found {
			res = r
		}
		if time.Now().After(deadline) {
			t.Skipf("slot %q not recorded invalid in time (is node-24 enabled + mis-imaged in this install?): last=%+v", slot, res)
		}
		time.Sleep(3 * time.Second)
	}
	t.Logf("validator marked %s invalid: %s", slot, res.Reason)

	// A package.json with no build script routes to node-24 (classifier rule 5).
	const name = "e2e-node"
	deployApp(t, base, name, map[string]string{"package.json": `{"name":"x","dependencies":{"express":"^4"}}`, "index.js": "x"})
	t.Cleanup(func() { deleteApp(base, name) })

	app := waitPhase(t, c, name, 90*time.Second, vibedv1.PhaseFailed, vibedv1.PhaseReady)
	if app.Status.Phase != vibedv1.PhaseFailed {
		t.Fatalf("node app on a mis-imaged slot phase=%q, want Failed (gate should have blocked it)", app.Status.Phase)
	}
	var gated bool
	for _, cond := range app.Status.Conditions {
		if cond.Reason == "TemplateInvalid" {
			gated = true
			t.Logf("gated as expected: %s", cond.Message)
		}
	}
	if !gated {
		t.Errorf("expected a TemplateInvalid condition, got %+v", app.Status.Conditions)
	}
}

// deployAppEgress deploys with a per-app egress allow-list and returns the app id.
func deployAppEgress(t *testing.T, base, name string, files map[string]string, allowedHosts []string) string {
	t.Helper()
	hosts, _ := json.Marshal(allowedHosts)
	meta := `{"name":"` + name + `","egress":{"allowed_hosts":` + string(hosts) + `}}`

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	src, _ := mw.CreateFormFile("source", "source.tar.gz")
	if _, err := src.Write(buildTarball(t, files)); err != nil {
		t.Fatalf("write source: %v", err)
	}
	f, _ := mw.CreateFormField("metadata")
	f.Write([]byte(meta))
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/deploy", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /v1/deploy: %v", err)
	}
	resp.Body.Close()
	return name
}

// TestClusterEgressAuthz validates the per-app egress decision end-to-end: a
// deployed app's bound pod IP is authorized for its allow-listed host and
// denied for anything else, as the Squid proxy would see it. Needs the
// egress-authz service reachable — set VIBED_E2E_AUTHZ_URL (e.g. via
// `kubectl port-forward svc/vibed-egress-authz 8090:8090`).
func TestClusterEgressAuthz(t *testing.T) {
	authzURL := os.Getenv("VIBED_E2E_AUTHZ_URL")
	if authzURL == "" {
		t.Skip("set VIBED_E2E_AUTHZ_URL (port-forward the egress-authz service) to run")
	}
	base := requireServer(t)
	c := k8sClient(t)
	const name = "e2e-egress"

	deployAppEgress(t, base, name, map[string]string{"index.html": "<h1>egress</h1>"}, []string{"example.com"})
	t.Cleanup(func() { deleteApp(base, name) })

	app := waitPhase(t, c, name, 120*time.Second, vibedv1.PhaseReady, vibedv1.PhaseFailed)
	if app.Status.PodIP == "" {
		t.Fatalf("no bound pod IP (phase %q)", app.Status.Phase)
	}

	assertAuthz(t, authzURL, app.Status.PodIP, "example.com", http.StatusOK)        // allow-listed
	assertAuthz(t, authzURL, app.Status.PodIP, "exfil.example.net", http.StatusForbidden) // not listed
}

func assertAuthz(t *testing.T, authzURL, src, host string, want int) {
	t.Helper()
	resp, err := http.Get(authzURL + "/authz?src=" + src + "&host=" + host)
	if err != nil {
		t.Fatalf("authz GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != want {
		t.Errorf("authz(src=%s host=%s) = %d, want %d", src, host, resp.StatusCode, want)
	}
}

func deleteApp(base, name string) {
	req, _ := http.NewRequest(http.MethodDelete, base+"/v1/apps/"+name, nil)
	if r, err := http.DefaultClient.Do(req); err == nil {
		r.Body.Close()
	}
}
