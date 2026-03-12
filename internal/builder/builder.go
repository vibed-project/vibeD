package builder

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/vibed-project/vibeD/internal/config"

	pack "github.com/buildpacks/pack/pkg/client"
	"github.com/buildpacks/pack/pkg/image"
	"github.com/buildpacks/pack/pkg/logging"
)

// BuildRequest describes what to build.
type BuildRequest struct {
	SourceDir string
	ImageName string
	Language  string            // "static", "nodejs", "python", "go", or "" for auto
	Env       map[string]string
	Publish   bool // Push to registry after build
}

// BuildResult contains the output of a successful build.
type BuildResult struct {
	ImageRef string
}

// Builder builds container images from source code.
type Builder interface {
	Build(ctx context.Context, req BuildRequest) (*BuildResult, error)
}

// PackBuilder uses Cloud Native Buildpacks (via the pack library) to build images.
type PackBuilder struct {
	builderImage string
	pullPolicy   string
	logger       *slog.Logger
}

// NewPackBuilder creates a new PackBuilder.
func NewPackBuilder(cfg config.BuilderConfig, logger *slog.Logger) *PackBuilder {
	return &PackBuilder{
		builderImage: cfg.Image,
		pullPolicy:   cfg.PullPolicy,
		logger:       logger,
	}
}

func (b *PackBuilder) Build(ctx context.Context, req BuildRequest) (*BuildResult, error) {
	b.logger.Info("building container image",
		"source", req.SourceDir,
		"image", req.ImageName,
		"builder", b.builderImage,
	)

	packLogger := logging.NewSimpleLogger(os.Stderr)

	packClient, err := pack.NewClient(
		pack.WithLogger(packLogger),
	)
	if err != nil {
		return nil, fmt.Errorf("creating pack client: %w", err)
	}

	buildEnv := make(map[string]string)
	for k, v := range req.Env {
		buildEnv[k] = v
	}

	pullPolicy, err := parsePullPolicy(b.pullPolicy)
	if err != nil {
		return nil, err
	}

	err = packClient.Build(ctx, pack.BuildOptions{
		Image:      req.ImageName,
		Builder:    b.builderImage,
		AppPath:    req.SourceDir,
		Publish:    req.Publish,
		Env:        buildEnv,
		PullPolicy: pullPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("pack build failed: %w", err)
	}

	b.logger.Info("build completed", "image", req.ImageName)

	return &BuildResult{
		ImageRef: req.ImageName,
	}, nil
}

func parsePullPolicy(policy string) (image.PullPolicy, error) {
	switch policy {
	case "always":
		return image.PullAlways, nil
	case "never":
		return image.PullNever, nil
	case "if-not-present", "":
		return image.PullIfNotPresent, nil
	default:
		return 0, fmt.Errorf("unknown pull policy %q", policy)
	}
}
