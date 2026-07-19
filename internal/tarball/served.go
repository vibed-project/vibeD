package tarball

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	// Fast path: when the caller hands us an *os.File (e.g. the deploy spool),
	// hard-link its inode into the store instead of copying the payload a
	// second time. The caller's file is only ever linked — never moved or
	// renamed — and the caller must treat the file's contents as immutable
	// once Put returns, since the stored blob may share its inode. Any failure
	// (cross-device link, unnamed file, nonzero read offset, exists races)
	// falls through to the byte-copy path below, which also preserves the
	// atomic-rename guarantee the fast path keeps.
	if f, ok := r.(*os.File); ok && s.linkInto(f, dest) {
		return s.publicBaseURL + BlobPathPrefix + objectName(id), nil
	}

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

// linkInto hard-links f's inode to a temp name next to dest and renames it
// into place, reporting whether it succeeded. False means "use the copy path":
// the file has no usable name, sits on another filesystem, or its read offset
// is not at the start — Put's contract is to store what the reader would yield
// (offset → EOF), and a link always captures the whole file, so the two only
// agree at offset 0. On the false return f's offset is untouched, so the
// caller can still io.Copy from it.
func (s *servedStore) linkInto(f *os.File, dest string) bool {
	name := f.Name()
	if name == "" {
		return false
	}
	if off, err := f.Seek(0, io.SeekCurrent); err != nil || off != 0 {
		return false
	}
	// Link to a temp name first, then rename over dest: link(2) fails if the
	// target exists, while rename atomically replaces it — same overwrite +
	// no-partial-blob semantics as the copy path.
	tmp := fmt.Sprintf("%s.lnk-%d-%d", dest, os.Getpid(), time.Now().UnixNano())
	if err := os.Link(name, tmp); err != nil {
		return false
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return false
	}
	return true
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
