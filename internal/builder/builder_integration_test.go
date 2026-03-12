//go:build integration

package builder_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibed-project/vibeD/internal/builder"
	"github.com/vibed-project/vibeD/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"log/slog"
)

// TestPackBuilder_BuildStaticSite is a slow integration test that exercises
// the real buildpacks builder. Skipped when running with -short.
func TestPackBuilder_BuildStaticSite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow buildpack test in short mode")
	}

	// Create a minimal static site in a temp directory
	srcDir := t.TempDir()
	indexHTML := `<!DOCTYPE html>
<html>
<head><title>Build Test</title></head>
<body><h1>Hello from buildpack test</h1></body>
</html>`

	err := os.WriteFile(filepath.Join(srcDir, "index.html"), []byte(indexHTML), 0644)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.BuilderConfig{
		Image:      "paketobuildpacks/builder-jammy-base:latest",
		PullPolicy: "if-not-present",
	}

	b := builder.NewPackBuilder(cfg, logger)

	ctx := context.Background()
	result, err := b.Build(ctx, builder.BuildRequest{
		SourceDir: srcDir,
		ImageName: "vibed-test-build:latest",
		Publish:   false,
	})

	require.NoError(t, err)
	assert.Equal(t, "vibed-test-build:latest", result.ImageRef)
}
