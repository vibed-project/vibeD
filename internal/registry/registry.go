package registry

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
)

// Registry handles container image push/pull operations.
type Registry struct {
	baseURL  string
	insecure bool
	logger   *slog.Logger
}

// NewRegistry creates a new Registry client.
func NewRegistry(baseURL string, insecure bool, logger *slog.Logger) *Registry {
	return &Registry{
		baseURL:  baseURL,
		insecure: insecure,
		logger:   logger,
	}
}

// Push pushes a locally-built image to the configured container registry.
func (r *Registry) Push(ctx context.Context, localImage string) (string, error) {
	r.logger.Info("pushing image to registry", "image", localImage, "registry", r.baseURL)

	// Read the image from the local Docker/Podman daemon
	ref, err := name.ParseReference(localImage)
	if err != nil {
		return "", fmt.Errorf("parsing image reference %q: %w", localImage, err)
	}

	img, err := daemon.Image(ref)
	if err != nil {
		return "", fmt.Errorf("reading local image %q: %w", localImage, err)
	}

	// Tag for the remote registry
	remoteRef := fmt.Sprintf("%s/%s", r.baseURL, localImage)
	remoteTag, err := name.ParseReference(remoteRef)
	if err != nil {
		return "", fmt.Errorf("parsing remote reference %q: %w", remoteRef, err)
	}

	// Push to registry
	opts := []crane.Option{crane.WithAuthFromKeychain(authn.DefaultKeychain), crane.WithContext(ctx)}
	if r.insecure {
		opts = append(opts, crane.Insecure)
	}
	if err := crane.Push(img, remoteTag.String(), opts...); err != nil {
		return "", fmt.Errorf("pushing to registry: %w", err)
	}

	// Get the digest for immutable reference
	digest, err := img.Digest()
	if err != nil {
		return remoteRef, nil // Return tag reference if digest fails
	}

	digestRef := fmt.Sprintf("%s@%s", remoteTag.Context().String(), digest.String())
	r.logger.Info("image pushed", "ref", digestRef)

	return digestRef, nil
}

// Pull pulls an image from the registry (for redeployment).
func (r *Registry) Pull(ctx context.Context, imageRef string) (v1.Image, error) {
	r.logger.Info("pulling image from registry", "ref", imageRef)

	opts := []crane.Option{crane.WithAuthFromKeychain(authn.DefaultKeychain), crane.WithContext(ctx)}
	if r.insecure {
		opts = append(opts, crane.Insecure)
	}
	img, err := crane.Pull(imageRef, opts...)
	if err != nil {
		return nil, fmt.Errorf("pulling image %q: %w", imageRef, err)
	}

	return img, nil
}

// ImageExists checks if an image exists in the registry.
func (r *Registry) ImageExists(ctx context.Context, imageRef string) (bool, error) {
	opts := []crane.Option{crane.WithAuthFromKeychain(authn.DefaultKeychain), crane.WithContext(ctx)}
	if r.insecure {
		opts = append(opts, crane.Insecure)
	}
	_, err := crane.Digest(imageRef, opts...)
	if err != nil {
		return false, nil
	}
	return true, nil
}
