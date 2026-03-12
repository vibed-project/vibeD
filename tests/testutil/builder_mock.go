//go:build integration

package testutil

import (
	"context"
	"sync"

	"github.com/vibed-project/vibeD/internal/builder"
)

// MockBuilder implements builder.Builder by returning a fixed image reference
// without actually running buildpacks. This makes tests fast (seconds instead of minutes).
// The returned image must be pre-loaded into the Kind cluster.
type MockBuilder struct {
	// ImageRef is the image reference to return from Build.
	// Defaults to TestImage if empty.
	ImageRef string

	// Err is an optional error to return from Build.
	Err error

	mu         sync.Mutex
	buildCalls int
}

// Build returns the configured ImageRef without running buildpacks.
func (m *MockBuilder) Build(_ context.Context, req builder.BuildRequest) (*builder.BuildResult, error) {
	m.mu.Lock()
	m.buildCalls++
	m.mu.Unlock()

	if m.Err != nil {
		return nil, m.Err
	}

	imageRef := m.ImageRef
	if imageRef == "" {
		imageRef = TestImage
	}

	return &builder.BuildResult{
		ImageRef: imageRef,
	}, nil
}

// BuildCalls returns how many times Build was called.
func (m *MockBuilder) BuildCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buildCalls
}
