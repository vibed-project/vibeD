package tarball

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vibed-project/vibeD/internal/config"
)

// servedStore writes tarballs to a local directory (a PVC in-cluster) and
// hands out in-cluster URLs that vibeD's own blob handler serves. The
// handler (see cmd/vibed) reads from the same BasePath.
type servedStore struct {
	basePath      string
	publicBaseURL string
}

func newServedStore(cfg config.ServedTarballConfig) (*servedStore, error) {
	if cfg.BasePath == "" {
		return nil, fmt.Errorf("tarball served: storage.tarball.served.basePath is required")
	}
	if cfg.PublicBaseURL == "" {
		return nil, fmt.Errorf("tarball served: storage.tarball.served.publicBaseURL is required (the in-cluster URL the agent dials, e.g. http://vibed.vibed-system.svc.cluster.local:8080)")
	}
	if err := os.MkdirAll(cfg.BasePath, 0o755); err != nil {
		return nil, fmt.Errorf("tarball served: creating basePath %q: %w", cfg.BasePath, err)
	}
	return &servedStore{
		basePath:      cfg.BasePath,
		publicBaseURL: strings.TrimRight(cfg.PublicBaseURL, "/"),
	}, nil
}

// BlobPathPrefix is the URL path the served blob handler mounts under and
// the agent fetches from.
const BlobPathPrefix = "/internal/sources/"

func (s *servedStore) Put(_ context.Context, id string, r io.Reader) (string, error) {
	if id == "" {
		return "", fmt.Errorf("tarball served: id is empty")
	}
	dest := filepath.Join(s.basePath, objectName(id))
	// Write to a temp file then rename so a reader never sees a partial blob.
	tmp, err := os.CreateTemp(s.basePath, ".put-*")
	if err != nil {
		return "", fmt.Errorf("tarball served: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return "", fmt.Errorf("tarball served: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("tarball served: close: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", fmt.Errorf("tarball served: rename: %w", err)
	}
	return s.publicBaseURL + BlobPathPrefix + objectName(id), nil
}

func (s *servedStore) Delete(_ context.Context, id string) error {
	err := os.Remove(filepath.Join(s.basePath, objectName(id)))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("tarball served: delete: %w", err)
	}
	return nil
}

// BasePath exposes the on-disk directory so the blob-serving HTTP handler
// can read from the same place. Returns "" for non-served stores.
func (s *servedStore) BasePath() string { return s.basePath }
