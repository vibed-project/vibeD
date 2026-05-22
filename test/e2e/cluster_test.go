//go:build e2ecluster

// cluster_test.go is the literal cluster E2E from refactor.md §10.2. Unlike
// slice_test.go (in-process, always runs), this drives a REAL deploy through a
// running stack: it POSTs a tarball to /v1/deploy and waits for the app to
// reach Ready. It needs a cluster with the full vibeD stack installed and the
// server reachable (set VIBED_E2E_URL, default http://localhost:18090):
//
//	# CI runs this via .github/workflows/e2e-cluster.yaml. Locally:
//	helm install vibed deploy/helm/vibed -n vibed-system --create-namespace \
//	     -f deploy/helm/vibed/values-kind.yaml --wait
//	kubectl port-forward -n vibed-system svc/vibed 18090:8080 &
//	make e2e-cluster
//
// Gated behind the e2ecluster build tag, and skips when the server isn't
// reachable, so it never breaks the default `go test`.
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
)

// baseURL is the vibed server under test; the workflow sets VIBED_E2E_URL.
func baseURL() string {
	if u := os.Getenv("VIBED_E2E_URL"); u != "" {
		return u
	}
	return "http://localhost:18090"
}

// requireServer skips unless the vibed server answers /healthz. The build tag
// already keeps this out of the default `go test`; this guard means a tagged
// run without a live server skips rather than fails.
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

// staticTarball builds a gzipped tar with a single index.html.
func staticTarball(t *testing.T, html string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte(html)
	if err := tw.WriteHeader(&tar.Header{Name: "index.html", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("tar write: %v", err)
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

type appResp struct {
	AppID string `json:"app_id"`
	Phase string `json:"phase"`
	URL   string `json:"url"`
}

// TestClusterDeployReachesReady drives a real deploy through the running stack:
// POST /v1/deploy with a static tarball, then poll /v1/apps/{id} until Ready.
// This exercises upload -> classify -> claim a warm sandbox -> inject source ->
// agent serves -> Ready against a real cluster with the chart installed.
func TestClusterDeployReachesReady(t *testing.T) {
	base := requireServer(t)
	const name = "e2e-static"

	// Multipart body: gzipped source tarball + metadata JSON.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	src, _ := mw.CreateFormFile("source", "source.tar.gz")
	if _, err := src.Write(staticTarball(t, "<!doctype html><h1>cluster e2e</h1>")); err != nil {
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
	// 200 = already Ready; 202 = accepted onto a slow path (poll). Both fine.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("deploy returned %d: %s", resp.StatusCode, raw)
	}
	var dr deployResp
	if err := json.Unmarshal(raw, &dr); err != nil || dr.AppID == "" {
		t.Fatalf("bad deploy response (%d): %s", resp.StatusCode, raw)
	}
	t.Cleanup(func() {
		dreq, _ := http.NewRequest(http.MethodDelete, base+"/v1/apps/"+dr.AppID, nil)
		if r, e := http.DefaultClient.Do(dreq); e == nil {
			r.Body.Close()
		}
	})

	deadline := time.Now().Add(120 * time.Second)
	for {
		gr, err := http.Get(base + "/v1/apps/" + dr.AppID)
		if err != nil {
			t.Fatalf("GET /v1/apps/%s: %v", dr.AppID, err)
		}
		b, _ := io.ReadAll(gr.Body)
		gr.Body.Close()
		var ar appResp
		_ = json.Unmarshal(b, &ar)
		switch ar.Phase {
		case "Ready":
			if ar.URL == "" {
				t.Fatal("Ready but no URL")
			}
			t.Logf("app %s Ready at %s", dr.AppID, ar.URL)
			return
		case "Failed":
			t.Fatalf("app %s Failed: %s", dr.AppID, b)
		}
		if time.Now().After(deadline) {
			t.Fatalf("app %s never reached Ready (last phase %q)", dr.AppID, ar.Phase)
		}
		time.Sleep(2 * time.Second)
	}
}
