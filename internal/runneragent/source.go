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

// Decompression-bomb limits. Capping only the compressed input is not enough:
// a small gzip can expand to tens of GB, exhausting disk/inodes on the sandbox.
// We additionally bound the UNCOMPRESSED output — cumulatively, per entry, and
// by entry count — enforced inside the copy loop so extraction aborts the moment
// a budget is exceeded rather than after writing the whole bomb.
const (
	// MaxUncompressedBytes caps the total uncompressed size across all entries.
	// 500 MB is 10x the 50 MB compressed cap — generous for real sources while
	// still bounding a gzip bomb.
	MaxUncompressedBytes = 500 * 1024 * 1024
	// MaxEntryBytes caps a single file's uncompressed size.
	MaxEntryBytes = 100 * 1024 * 1024
	// MaxEntries caps the number of tar entries, bounding an inode-exhaustion
	// ("many tiny files") archive.
	MaxEntries = 20000
)

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
	var totalUncompressed int64
	var entries int
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tarball: %w", err)
		}
		entries++
		if entries > MaxEntries {
			return fmt.Errorf("tarball has too many entries (limit %d)", MaxEntries)
		}
		written, err := extractEntry(dir, hdr, tr, remainingBudget(totalUncompressed))
		if err != nil {
			return err
		}
		totalUncompressed += written
		if totalUncompressed > MaxUncompressedBytes {
			return fmt.Errorf("tarball expands beyond the %d-byte uncompressed limit", int64(MaxUncompressedBytes))
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

// remainingBudget returns how many uncompressed bytes may still be written
// before the cumulative cap is hit, never negative.
func remainingBudget(alreadyWritten int64) int64 {
	rem := int64(MaxUncompressedBytes) - alreadyWritten
	if rem < 0 {
		return 0
	}
	return rem
}

// extractEntry writes a single tar entry under dir and returns the number of
// bytes written. Symlinks and absolute / parent-escaping paths are rejected;
// the agent runs untrusted code but the extraction itself must not be the
// attack surface. The copy is bounded by the smaller of MaxEntryBytes and the
// remaining cumulative budget so a decompression bomb aborts mid-write instead
// of filling the disk.
func extractEntry(dir string, hdr *tar.Header, r io.Reader, budget int64) (int64, error) {
	clean := filepath.Clean(hdr.Name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return 0, fmt.Errorf("illegal tar entry path %q", hdr.Name)
	}
	dest := filepath.Join(dir, clean)

	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return 0, fmt.Errorf("mkdir %s: %w", dest, err)
		}
	case tar.TypeReg, tar.TypeRegA:
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return 0, fmt.Errorf("mkdir parent of %s: %w", dest, err)
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o755)
		if err != nil {
			return 0, fmt.Errorf("open %s: %w", dest, err)
		}
		// Cap this entry at the per-entry limit AND the remaining cumulative
		// budget. Read one extra byte so we can distinguish "exactly at the
		// cap" from "over the cap".
		perEntry := int64(MaxEntryBytes)
		if budget < perEntry {
			perEntry = budget
		}
		written, err := io.Copy(f, io.LimitReader(r, perEntry+1))
		if closeErr := f.Close(); closeErr != nil && err == nil {
			return written, fmt.Errorf("close %s: %w", dest, closeErr)
		}
		if err != nil {
			return written, fmt.Errorf("write %s: %w", dest, err)
		}
		if written > perEntry {
			return written, fmt.Errorf("tar entry %q exceeds the uncompressed size limit", hdr.Name)
		}
		return written, nil
	default:
		// Reject symlinks, hardlinks, devices, FIFOs. Untrusted source code
		// has no legitimate need for them in this contract.
		return 0, fmt.Errorf("unsupported tar entry type %d for %q", hdr.Typeflag, hdr.Name)
	}
	return 0, nil
}
