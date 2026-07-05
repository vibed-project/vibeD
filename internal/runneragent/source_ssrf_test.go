package runneragent

import (
	"archive/tar"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExtractEntryContainment is the proof for #64: extractEntry rejects any
// entry whose resolved path escapes the destination dir — matching (and no
// weaker than) the sibling writeFiles filepath.Rel containment check.
func TestExtractEntryContainment(t *testing.T) {
	dir := t.TempDir()
	escapes := []string{
		"../evil.txt",
		"../../evil.txt",
		"a/../../evil.txt",
		"/abs/evil.txt",
		"..",
	}
	for _, name := range escapes {
		hdr := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}
		if _, err := extractEntry(dir, hdr, strings.NewReader("x"), MaxEntryBytes); err == nil {
			t.Errorf("extractEntry(%q) should be rejected as path escape", name)
		}
	}
	// A legitimate nested path is accepted.
	hdr := &tar.Header{Name: "sub/dir/ok.txt", Typeflag: tar.TypeReg, Mode: 0o644}
	if _, err := extractEntry(dir, hdr, strings.NewReader("hi"), MaxEntryBytes); err != nil {
		t.Errorf("legitimate nested entry rejected: %v", err)
	}
}

// TestFetchRejectsNonHTTPSScheme: with the scheme guard on (default), an http://
// or file:// source URL is rejected before any connection is made.
func TestFetchRejectsNonHTTPSScheme(t *testing.T) {
	f := &SourceFetcher{AllowPrivateHosts: true} // scheme guard ON
	for _, u := range []string{"http://example.com/x.tgz", "file:///etc/passwd", "ftp://host/x"} {
		err := f.Fetch(context.Background(), u, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("Fetch(%q) = %v, want an https-required error", u, err)
		}
	}
}

// TestFetchDialGuardBlocksPrivateAddress: with the dial guard on, connecting to
// a private/loopback address is refused at DIAL time even when the scheme guard
// is relaxed. The httptest server binds 127.0.0.1, which the guard must block.
func TestFetchDialGuardBlocksPrivateAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarballFromMap(t, map[string]string{"a.txt": "x"}))
	}))
	defer srv.Close()

	// Scheme guard relaxed (server is http on 127.0.0.1), dial guard ON.
	f := &SourceFetcher{AllowInsecureScheme: true, AllowPrivateHosts: false}
	err := f.Fetch(context.Background(), srv.URL, t.TempDir())
	if err == nil {
		t.Fatal("expected a dial-guard error connecting to 127.0.0.1, got nil")
	}
	if !strings.Contains(err.Error(), "private/link-local") {
		t.Fatalf("expected private-address refusal, got %v", err)
	}
}

// TestGuardedCheckRedirect: the redirect check enforces https on every hop and
// caps the chain length — so a public https URL can't 30x-bounce the fetcher
// onto http:// (e.g. an http metadata URL).
func TestGuardedCheckRedirect(t *testing.T) {
	check := guardedCheckRedirect(false) // production: https-only

	httpsHop, _ := http.NewRequest("GET", "https://cdn.example.com/x.tgz", nil)
	if err := check(httpsHop, nil); err != nil {
		t.Errorf("https redirect should be allowed, got %v", err)
	}

	httpHop, _ := http.NewRequest("GET", "http://169.254.169.254/latest/meta-data/", nil)
	if err := check(httpHop, nil); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Errorf("http redirect should be rejected, got %v", err)
	}

	// Too many redirects is capped.
	via := make([]*http.Request, 10)
	if err := check(httpsHop, via); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("long redirect chain should be rejected, got %v", err)
	}

	// With the scheme guard relaxed (tests), http redirects are allowed.
	if err := guardedCheckRedirect(true)(httpHop, nil); err != nil {
		t.Errorf("relaxed redirect check should allow http, got %v", err)
	}
}

// TestGuardedDialContextBlocksMetadata: the dial guard refuses the metadata IP
// and private ranges but allows a public address to proceed to the real dialer.
func TestGuardedDialContextBlocksMetadata(t *testing.T) {
	dial := guardedDialContext(false)
	for _, addr := range []string{"169.254.169.254:80", "10.0.0.5:443", "127.0.0.1:8080", "[::1]:443"} {
		if _, err := dial(context.Background(), "tcp", addr); err == nil || !strings.Contains(err.Error(), "private/link-local") {
			t.Errorf("dial(%s) should be blocked, got %v", addr, err)
		}
	}
	// A malformed address is refused too.
	if _, err := dial(context.Background(), "tcp", "no-port"); err == nil {
		t.Error("malformed dial address should error")
	}
}
