//go:build integration

package testutil

import (
	"github.com/vibed-project/vibeD/internal/orchestrator"
)

// TestImage is the container image used by MockBuilder.
// It must be pre-loaded into the Kind cluster via `make test-integration-setup`.
const TestImage = "docker.io/library/nginx:1.27-alpine"

// SampleHTMLFiles returns a minimal set of source files for testing.
func SampleHTMLFiles() map[string]string {
	return map[string]string{
		"index.html": `<!DOCTYPE html>
<html>
<head><title>vibeD Test</title></head>
<body><h1>Hello from vibeD integration test</h1></body>
</html>`,
	}
}

// SampleNodeFiles returns a minimal Node.js app for testing.
func SampleNodeFiles() map[string]string {
	return map[string]string{
		"package.json": `{
  "name": "vibed-test-app",
  "version": "1.0.0",
  "scripts": { "start": "node server.js" },
  "dependencies": {}
}`,
		"server.js": `const http = require('http');
const port = process.env.PORT || 8080;
http.createServer((req, res) => {
  res.writeHead(200);
  res.end('Hello from vibeD test');
}).listen(port);`,
	}
}

// SampleDeployRequest returns a DeployRequest suitable for integration testing.
func SampleDeployRequest(name string) orchestrator.DeployRequest {
	return orchestrator.DeployRequest{
		Name:     name,
		Files:    SampleHTMLFiles(),
		Language: "html",
		Target:   "kubernetes",
		Port:     80,
	}
}
