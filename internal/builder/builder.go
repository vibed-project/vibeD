package builder

import (
	"context"
	"fmt"
	"regexp"
)

// BuildRequest describes what to build.
type BuildRequest struct {
	SourceDir string
	ImageName string
	Namespace string
	Language  string // "static", "nodejs", "python", "go", or "" for auto
	Env       map[string]string
	Publish   bool // Push to registry after build
}

// BuildResult contains the output of a successful build.
type BuildResult struct {
	ImageRef string
	// Digest is the immutable manifest digest of the pushed image
	// (e.g. "sha256:abc..."). When set, deployers should pin via
	// "ImageRef@Digest" instead of the tag to avoid registry-cache
	// surprises and make Knative revisions reproducibly different.
	Digest string
}

// Builder builds container images from source code.
type Builder interface {
	Build(ctx context.Context, req BuildRequest) (*BuildResult, error)
	// PublishesInternally returns true when the builder handles registry push
	// as part of Build (e.g. Buildah Job). When true the orchestrator skips
	// the separate crane-based registry.Push step.
	PublishesInternally() bool
}

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
