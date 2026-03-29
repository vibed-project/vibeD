package builder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"

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
	// PublishesInternally returns true when the builder handles registry push
	// as part of Build (e.g. Buildah Job). When true the orchestrator skips
	// the separate crane-based registry.Push step.
	PublishesInternally() bool
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

// PublishesInternally returns false: PackBuilder builds into the local daemon; the
// orchestrator handles the separate registry.Push step via crane when needed.
func (b *PackBuilder) PublishesInternally() bool { return false }

// validImageName matches standard OCI image references: registry/repo:tag or registry/repo
// Rejects characters that could be used for shell injection (spaces, semicolons, backticks, etc.)
var validImageName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$`)

// validateImageName ensures an image name is safe to use in shell commands.
func validateImageName(name string) error {
	if name == "" {
		return fmt.Errorf("image name is required")
	}
	if len(name) > 512 {
		return fmt.Errorf("image name too long (max 512 chars)")
	}
	if !validImageName.MatchString(name) {
		return fmt.Errorf("image name %q contains invalid characters", name)
	}
	return nil
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
