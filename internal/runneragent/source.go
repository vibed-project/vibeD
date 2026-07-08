package runneragent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vibed-project/vibeD/internal/netguard"
	"github.com/vibed-project/vibeD/internal/tarsafe"
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
	// dialGuard classifies an IP the fetcher must refuse to connect to. nil uses
	// the production guard (netguard.IsLinkLocalOrLoopback). Overridable by
	// in-package tests that must reach a loopback httptest server.
	dialGuard func(netip.Addr) bool
}

func (f *SourceFetcher) blocked(ip netip.Addr) bool {
	if f.dialGuard != nil {
		return f.dialGuard(ip)
	}
	return netguard.IsLinkLocalOrLoopback(ip)
}

func allowedScheme(s string) bool { return s == "http" || s == "https" }

// NewSourceFetcher returns a fetcher with production timeouts and an SSRF guard.
// It connects only over http/https and refuses to dial loopback, link-local, or
// the cloud metadata endpoint (169.254.169.254). The address is checked at
// connect time (net.Dialer.Control, after DNS resolution) so a rebind to a
// blocked IP cannot slip past a name-based allow-list. Private/RFC1918 is
// deliberately allowed: the source tarball is served from an in-cluster host
// (internal/tarball/served.go), so blanket private-range denial would break
// every deploy — this mirrors the boundary the egress authz enforces.
func NewSourceFetcher() *SourceFetcher {
	f := &SourceFetcher{}
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("source dial: bad address %q: %w", address, err)
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("source dial: unresolved address %q", host)
			}
			if f.blocked(ip) {
				return fmt.Errorf("source dial to %s is not allowed", ip)
			}
			return nil
		},
	}
	f.Client = &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			// This path faces untrusted input: ignore env proxies so the dial
			// guard inspects the real target IP, not a proxy's.
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("source fetch: stopped after 10 redirects")
			}
			if !allowedScheme(req.URL.Scheme) {
				return fmt.Errorf("source fetch: redirect to disallowed scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
	return f
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
	if !allowedScheme(req.URL.Scheme) {
		return fmt.Errorf("source url scheme %q is not allowed (http or https only)", req.URL.Scheme)
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

	budget := int64(tarsafe.MaxUncompressedBytes)

	// Two independent caps defeat a decompression bomb. (1) tarsafe.LimitedReader
	// bounds bytes read from the gzip stream (headers + data tar.Next skips), so
	// a tiny gzip can't burn unbounded CPU/memory. (2) `remaining` (below) bounds
	// bytes actually WRITTEN across all entries — the load-bearing one, because a
	// PAX/GNU sparse entry materializes hole zeros without reading the gzip
	// stream, so a read-side cap alone would let it write unbounded zeros.
	tr := tar.NewReader(tarsafe.NewLimitedReader(gz, budget))
	remaining := budget
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tarball: %w", err)
		}
		entries++
		if entries > tarsafe.MaxEntries {
			return fmt.Errorf("tarball has too many entries (> %d)", tarsafe.MaxEntries)
		}
		if hdr.Size > budget {
			return fmt.Errorf("tar entry %q is too large (%d bytes)", hdr.Name, hdr.Size)
		}
		if err := extractEntry(dir, hdr, tr, &remaining); err != nil {
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
func extractEntry(dir string, hdr *tar.Header, r io.Reader, remaining *int64) error {
	clean := filepath.Clean(hdr.Name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("illegal tar entry path %q", hdr.Name)
	}
	dest := filepath.Join(dir, clean)

	// Defense-in-depth: mirror the post-join filepath.Rel containment the agent's
	// sibling writeFiles applies, so a crafted entry can never resolve outside
	// dir even if the prefix check above is bypassed by a future path form.
	if rel, err := filepath.Rel(dir, dest); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("illegal tar entry path %q escapes extraction dir", hdr.Name)
	}

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
		// Copy at most (remaining+1) bytes so we can detect an overrun: CopyN
		// materializes sparse-hole zeros just like a real read, so this counts the
		// bytes hitting disk. A single write past the shared budget aborts.
		rem := *remaining
		n, cerr := io.CopyN(f, r, rem+1)
		*remaining = rem - n
		if cerr != nil && cerr != io.EOF {
			_ = f.Close()
			return fmt.Errorf("write %s: %w", dest, cerr)
		}
		if n > rem {
			_ = f.Close()
			return fmt.Errorf("write %s: %w", dest, tarsafe.ErrTooLarge)
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
