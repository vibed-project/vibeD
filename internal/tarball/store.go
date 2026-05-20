// Package tarball stores the gzipped source tarball uploaded to
// POST /v1/deploy and returns a URL that vibed-agent can pull from.
//
// Two backends, selected by config.storage.tarball.backend:
//
//   - "served": writes the blob to a directory on vibeD's own volume and
//     returns an in-cluster URL (GET /internal/sources/<id>.tar.gz served by
//     vibeD itself). No extra infrastructure — the dev default.
//   - "s3": streams the blob to S3/MinIO and returns a pre-signed GET URL.
//     The production path; keeps the control plane stateless.
//
// Both satisfy Store; the deploy handler is agnostic to which is wired.
package tarball

import (
	"context"
	"fmt"
	"io"

	"github.com/vibed-project/vibeD/internal/config"
)

// Store persists a source tarball under an app id and returns a URL the
// in-cluster agent can fetch it from.
type Store interface {
	// Put writes the tarball bytes for id and returns a fetch URL. Implementations
	// overwrite any existing blob for the same id (redeploys reuse the id).
	Put(ctx context.Context, id string, r io.Reader) (refURL string, err error)
	// Delete removes the stored tarball for id. A missing blob is not an error.
	Delete(ctx context.Context, id string) error
}

// New constructs the Store selected by cfg. Defaults to the served backend
// when cfg.Backend is empty.
func New(cfg config.TarballConfig) (Store, error) {
	switch cfg.Backend {
	case "", "served":
		return newServedStore(cfg.Served)
	case "s3":
		return newS3Store(cfg.S3)
	default:
		return nil, fmt.Errorf("tarball: unknown backend %q (want served|s3)", cfg.Backend)
	}
}

// objectName is the canonical blob name for an app id. Centralized so the
// served HTTP handler and the stores agree.
func objectName(id string) string {
	return id + ".tar.gz"
}
