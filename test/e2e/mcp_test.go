//go:build e2ecluster

// mcp_test.go exercises vibeD's MCP server end-to-end against the same
// cluster the rest of the e2e suite uses. It opens a Streamable-HTTP MCP
// session to /mcp, lists the tool surface, calls deploy_artifact with a
// realistic source map (static site + Python app), polls get_artifact_status
// until the artifact goes Running, and HTTP-GETs the resulting URL to
// confirm the app is actually serving. This is the test that would have
// caught a tool-schema drift, a transport-layer regression, or a controller
// bug that only manifests when the request comes in via MCP rather than the
// /v1 REST API.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpEndpoint is the MCP Streamable-HTTP URL on the vibed server.
func mcpEndpoint() string { return baseURL() + "/mcp" }

// mcpConnect dials the MCP server and returns a live session. Fails (strict)
// or skips (local) when the server isn't reachable, matching the rest of the
// e2e helpers.
func mcpConnect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	requireServer(t) // base health-check first; cheaper failure surface than a hung MCP handshake
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cli := mcp.NewClient(&mcp.Implementation{Name: "vibed-e2e", Version: "0.0.0"}, nil)
	cs, err := cli.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: mcpEndpoint()}, nil)
	if err != nil {
		failOrSkip(t, "MCP connect %s: %v", mcpEndpoint(), err)
		return nil
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool invokes a tool and decodes its structuredContent into out. Returns
// an error result (IsError) as a Go error so the test reads naturally.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		var msg strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg.WriteString(tc.Text)
			}
		}
		t.Fatalf("tool %s returned error: %s", name, msg.String())
	}
	if out != nil && res.StructuredContent != nil {
		// SDK leaves StructuredContent as a raw json.RawMessage when the
		// caller didn't supply a typed schema, but a map[string]any round-
		// trip works for either shape.
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s result: %v\nraw: %s", name, err, string(raw))
		}
	}
}

// pollUntilRunning polls get_artifact_status until phase=="running" or the
// deadline elapses. Returns the URL the artifact serves on (which the test
// then HTTP-GETs to prove the deploy actually works end-to-end).
func pollUntilRunning(t *testing.T, cs *mcp.ClientSession, id string, deadline time.Duration) string {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		var got struct {
			Status string `json:"status"`
			URL    string `json:"url"`
		}
		callTool(t, cs, "get_artifact_status", map[string]any{"artifact_id": id}, &got)
		switch got.Status {
		case "running":
			if got.URL == "" {
				t.Fatalf("artifact %s is running but URL is empty", id)
			}
			return got.URL
		case "failed":
			t.Fatalf("artifact %s went to failed state", id)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("artifact %s did not reach running within %s", id, deadline)
	return ""
}

// httpGetOK confirms the deployed URL actually responds 2xx with the
// expected body fragment. Wraps a short retry loop for the brief window
// between status=running and ingress route propagation.
func httpGetOK(t *testing.T, url, wantBody string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && (wantBody == "" || strings.Contains(string(body), wantBody)) {
			return
		}
		lastErr = fmt.Errorf("status=%d body=%q", resp.StatusCode, string(body))
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("GET %s never returned 2xx with %q: %v", url, wantBody, lastErr)
}

// TestMCPToolsListAdvertisesDeploy is the cheapest possible MCP smoke test:
// initialize the session and assert deploy_artifact + get_artifact_status
// are on the tool surface. Catches a tool-registration regression without
// needing a live cluster — though it still requires the server to be up.
func TestMCPToolsListAdvertisesDeploy(t *testing.T) {
	cs := mcpConnect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{"deploy_artifact": false, "get_artifact_status": false}
	for _, tool := range tools.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q missing from MCP server's tool surface", name)
		}
	}
}

// TestMCPDeployStaticSite drives the static-site flow through MCP end-to-end:
// deploy_artifact → poll get_artifact_status → HTTP-GET the URL and confirm
// the page renders. The deploy uses the static-nginx slot the e2e overlay
// configures, which is the only general-purpose slot built into the kind
// cluster. A regression in MCP tool dispatch, deploy classification, or the
// router/Caddy URL hand-off would fail this test.
func TestMCPDeployStaticSite(t *testing.T) {
	cs := mcpConnect(t)
	const name = "mcp-static"

	t.Cleanup(func() { _, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "delete_artifact", Arguments: map[string]any{"artifact_id": name},
	}) })

	var out struct {
		ArtifactID string `json:"artifact_id"`
		Status     string `json:"status"`
		URL        string `json:"url"`
	}
	callTool(t, cs, "deploy_artifact", map[string]any{
		"name":     name,
		"language": "static",
		"files": map[string]string{
			"index.html": `<!doctype html><title>e2e</title><h1>vibed-mcp-e2e-static</h1>`,
			"style.css":  `body{font-family:system-ui}`,
		},
	}, &out)
	if out.ArtifactID == "" {
		t.Fatal("deploy_artifact returned empty artifact_id")
	}
	url := out.URL
	if out.Status != "running" || url == "" {
		url = pollUntilRunning(t, cs, out.ArtifactID, 3*time.Minute)
	}
	httpGetOK(t, url, "vibed-mcp-e2e-static")
}

// TestMCPDeployPythonApp covers the dynamic-runtime path: a tiny Python
// stdlib HTTP server gets deployed, classified as python, claimed against
// the python-313 warm pool (when present), and reachable via its URL. When
// no python slot is provisioned the deploy fails with a clear TemplateMissing
// — we skip in that case rather than fail, since the e2e overlay is opt-in
// per-runtime.
func TestMCPDeployPythonApp(t *testing.T) {
	cs := mcpConnect(t)
	const name = "mcp-python"

	t.Cleanup(func() { _, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "delete_artifact", Arguments: map[string]any{"artifact_id": name},
	}) })

	pyMain := `import os
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(b"vibed-mcp-e2e-python")
        return

if __name__ == "__main__":
    port = int(os.environ.get("PORT", "8080"))
    HTTPServer(("", port), H).serve_forever()
`

	var out struct {
		ArtifactID string `json:"artifact_id"`
		Status     string `json:"status"`
		URL        string `json:"url"`
	}
	// Don't fail-out on the deploy call itself: if the slot isn't provisioned
	// (default e2e overlay only enables node-24 + static-nginx), the server
	// returns an error we should translate to a skip — there's nothing
	// MCP-specific to test in that case.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "deploy_artifact",
		Arguments: map[string]any{
			"name":     name,
			"language": "python",
			"port":     8080,
			"files": map[string]string{
				"main.py":          pyMain,
				"requirements.txt": "",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool deploy_artifact: %v", err)
	}
	if res.IsError {
		msg := ""
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				msg = tc.Text
			}
		}
		if strings.Contains(strings.ToLower(msg), "no warm pool") || strings.Contains(strings.ToLower(msg), "template") {
			t.Skipf("python slot not provisioned in this e2e overlay: %s", msg)
		}
		t.Fatalf("deploy_artifact failed: %s", msg)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode result: %v\nraw: %s", err, string(raw))
	}

	url := out.URL
	if out.Status != "running" || url == "" {
		url = pollUntilRunning(t, cs, out.ArtifactID, 5*time.Minute) // python pulls are slower
	}
	httpGetOK(t, url, "vibed-mcp-e2e-python")
}
