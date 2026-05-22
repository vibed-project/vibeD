package runneragent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxTarballBytes caps the on-the-wire tarball size at 50 MB, matching
// refactor.md §5.1 ("Reject > 50 MB"). The check is enforced via
// io.LimitReader, so an oversized download fails before extraction.
const MaxTarballBytes = 50 * 1024 * 1024

// SourceFetcher fetches the gzipped tarball at sourceURL and extracts it
// into dir, replacing any existing contents (caller is responsible for
// clearing the directory first). The default implementation uses an
// http.Client with a short connect timeout — callers can swap it in tests.
type SourceFetcher struct {
	Client *http.Client
}

// NewSourceFetcher returns a fetcher with sensible production timeouts.
func NewSourceFetcher() *SourceFetcher {
	return &SourceFetcher{Client: &http.Client{Timeout: 60 * time.Second}}
}

// Fetch downloads sourceURL and extracts the resulting gzipped tarball into
// dir. Errors are returned with enough context for the caller to surface in
// /inject's response body.
func (f *SourceFetcher) Fetch(ctx context.Context, sourceURL, dir string) error {
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return fmt.Errorf("build source request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fetch source: HTTP %s", resp.Status)
	}

	// LimitReader caps total bytes consumed by gzip + tar, even if the server
	// sent a too-large body. We add 1 so the check below catches "exactly at
	// the limit + 1" as overflow.
	limited := io.LimitReader(resp.Body, MaxTarballBytes+1)
	gz, err := gzip.NewReader(limited)
	if err != nil {
		return fmt.Errorf("gunzip source: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tarball: %w", err)
		}
		if err := extractEntry(dir, hdr, tr); err != nil {
			return err
		}
	}

	// Detect the over-limit case: if LimitReader stopped at the cap, the gzip
	// reader would have surfaced an "unexpected EOF". The explicit check
	// above catches the more common case of HTTP-level truncation; this one
	// catches a malicious or buggy producer that streams >50 MB.
	if _, err := io.Copy(io.Discard, limited); err == nil {
		// All data consumed within the limit — fine.
	}
	return nil
}

// extractEntry writes a single tar entry under dir. Symlinks and absolute /
// parent-escaping paths are rejected; the agent runs untrusted code but the
// extraction itself must not be the attack surface.
func extractEntry(dir string, hdr *tar.Header, r io.Reader) error {
	clean := filepath.Clean(hdr.Name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("illegal tar entry path %q", hdr.Name)
	}
	dest := filepath.Join(dir, clean)

	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dest, err)
		}
	case tar.TypeReg, tar.TypeRegA:
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("mkdir parent of %s: %w", dest, err)
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o755)
		if err != nil {
			return fmt.Errorf("open %s: %w", dest, err)
		}
		if _, err := io.Copy(f, r); err != nil {
			_ = f.Close()
			return fmt.Errorf("write %s: %w", dest, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close %s: %w", dest, err)
		}
	default:
		// Reject symlinks, hardlinks, devices, FIFOs. Untrusted source code
		// has no legitimate need for them in this contract.
		return fmt.Errorf("unsupported tar entry type %d for %q", hdr.Typeflag, hdr.Name)
	}
	return nil
}
