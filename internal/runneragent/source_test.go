package runneragent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/tarsafe"
)

// loopbackFetcher relaxes the SSRF dial guard so a test can reach a loopback
// httptest server; production NewSourceFetcher refuses loopback/link-local.
func loopbackFetcher() *SourceFetcher {
	f := NewSourceFetcher()
	f.dialGuard = func(netip.Addr) bool { return false }
	return f
}

// tarballFromMap builds a deterministic gzipped tarball whose entries match
// files (relative path → content). Useful for end-to-end SourceURL tests.
func tarballFromMap(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
			ModTime:  time.Unix(0, 0),
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gz: %v", err)
	}
	return buf.Bytes()
}

func TestSourceFetcherExtractsTarball(t *testing.T) {
	tarball := tarballFromMap(t, map[string]string{
		"app.py":           "print('hi')\n",
		"sub/data.json":    `{"k":"v"}`,
		"requirements.txt": "flask\n",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := loopbackFetcher().Fetch(context.Background(), srv.URL, dir); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for path, want := range map[string]string{
		"app.py":           "print('hi')\n",
		"sub/data.json":    `{"k":"v"}`,
		"requirements.txt": "flask\n",
	} {
		got, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

func TestSourceFetcherRejectsPathEscape(t *testing.T) {
	tarball := tarballFromMap(t, map[string]string{"../escape.txt": "bad"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	err := loopbackFetcher().Fetch(context.Background(), srv.URL, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "illegal tar entry") {
		t.Fatalf("expected illegal-tar-entry error, got %v", err)
	}
}

func TestSourceFetcherRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	err := loopbackFetcher().Fetch(context.Background(), srv.URL, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 surfaced, got %v", err)
	}
}

func TestSourceFetcherBlocksMetadataAndLoopbackDial(t *testing.T) {
	// The production guard refuses the cloud metadata endpoint and loopback at
	// dial time — Control fires before any connection is made.
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:1/x.tgz",
	} {
		err := NewSourceFetcher().Fetch(context.Background(), target, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("Fetch(%s): want a blocked-dial error, got %v", target, err)
		}
	}
}

func TestSourceFetcherGuardAllowsPrivateBlocksMetadata(t *testing.T) {
	// In-cluster RFC1918 (the served tarball store) must stay reachable; the
	// metadata/link-local and loopback endpoints must not.
	f := NewSourceFetcher()
	for _, ok := range []string{"10.0.0.5", "192.168.1.1", "172.16.0.9"} {
		if f.blocked(netip.MustParseAddr(ok)) {
			t.Errorf("guard blocked in-cluster private address %s", ok)
		}
	}
	for _, bad := range []string{"169.254.169.254", "127.0.0.1", "::1", "fe80::1", "fd00:ec2::254"} {
		if !f.blocked(netip.MustParseAddr(bad)) {
			t.Errorf("guard failed to block %s", bad)
		}
	}
}

// TestExtractEntryEnforcesCumulativeWriteBudget checks the load-bearing
// decompression-bomb guard: bytes actually written are capped across entries.
// This is what defeats PAX/GNU sparse entries, which materialize hole zeros
// without reading the compressed stream, so a read-side cap alone misses them.
func TestExtractEntryEnforcesCumulativeWriteBudget(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range []string{"a.bin", "b.bin"} {
		body := bytes.Repeat([]byte("x"), 100)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(&buf)
	dir := t.TempDir()
	remaining := int64(150) // fits one 100-byte entry, not both
	var err error
	for {
		hdr, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatalf("next: %v", e)
		}
		if err = extractEntry(dir, hdr, tr, &remaining); err != nil {
			break
		}
	}
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected a cumulative write-budget error, got %v", err)
	}
}

func TestSourceFetcherRejectsNonHTTPScheme(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x.tgz", "gopher://x/1"} {
		err := NewSourceFetcher().Fetch(context.Background(), u, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "scheme") {
			t.Fatalf("Fetch(%s): want a scheme error, got %v", u, err)
		}
	}
}

// bombHeaderTarball builds a gzipped stream with a single tar header that
// declares a huge size but has no matching body — enough for tar.Reader.Next to
// surface the oversized declared size so the per-entry cap rejects it without
// inflating hundreds of MB.
func bombHeaderTarball(t *testing.T, declaredSize int64) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	_ = tw.WriteHeader(&tar.Header{Name: "bomb.bin", Mode: 0o644, Size: declaredSize, Typeflag: tar.TypeReg})
	// Deliberately skip the body + Close: only the 512-byte header is needed.
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	return gz.Bytes()
}

func TestSourceFetcherRejectsDecompressionBomb(t *testing.T) {
	bomb := bombHeaderTarball(t, tarsafe.MaxUncompressedBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bomb)
	}))
	defer srv.Close()

	err := loopbackFetcher().Fetch(context.Background(), srv.URL, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected the oversized entry to be rejected, got %v", err)
	}
}

// TestInjectWithSourceURL drives the full Agent.handleInject path with a
// SourceURL: the agent fetches the tarball, extracts into the workdir, and
// starts the user process.
func TestInjectWithSourceURL(t *testing.T) {
	tarball := tarballFromMap(t, map[string]string{
		"hello.txt": "from-tarball",
	})
	tarServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer tarServer.Close()

	a := New(Config{
		Workdir:   t.TempDir(),
		AppPort:   8080,
		StopGrace: 200 * time.Millisecond,
	})
	a.Fetcher.dialGuard = func(netip.Addr) bool { return false } // reach the loopback tarServer
	t.Cleanup(func() { a.stopProcess() })

	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	c := NewClient(srv.URL, "")
	st, err := c.Inject(context.Background(), InjectRequest{
		SourceURL: tarServer.URL,
		Command:   []string{"sh", "-c", "cat hello.txt; sleep 5"},
	})
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if st.State != StateRunning {
		t.Fatalf("state=%q want running", st.State)
	}

	// Verify the file landed in the workdir.
	got, err := os.ReadFile(filepath.Join(a.cfg.Workdir, "hello.txt"))
	if err != nil {
		t.Fatalf("read injected file: %v", err)
	}
	if string(got) != "from-tarball" {
		t.Errorf("hello.txt = %q, want %q", got, "from-tarball")
	}

	// Logs should eventually contain the file content the user process printed.
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := c.Logs(context.Background(), 0)
		if err == nil {
			joined := strings.Join(got.Lines, "\n")
			if strings.Contains(joined, "from-tarball") {
				return
			}
		}
		if time.Now().After(deadline) {
			snapshot, _ := c.Logs(context.Background(), 0)
			t.Fatalf("user process never logged file content: %+v (last err=%v)", snapshot, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stageEntries lists .vibed-stage-* entries in dir. No *testing.T: it is also
// called from HTTP-handler goroutines, where t.Fatalf is not allowed.
func stageEntries(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var got []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".vibed-stage-") {
			got = append(got, e.Name())
		}
	}
	return got
}

// sourceURLAgent returns an Agent (loopback dial guard relaxed) on workdir and
// an httptest server driving its control handler.
func sourceURLAgent(t *testing.T, workdir string) (*Agent, *Client) {
	t.Helper()
	a := New(Config{Workdir: workdir, AppPort: 8080, StopGrace: 200 * time.Millisecond})
	a.Fetcher.dialGuard = func(netip.Addr) bool { return false }
	t.Cleanup(func() { a.stopProcess() })
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	return a, NewClient(srv.URL, "")
}

// TestInjectStagesInsideWorkdirAndLeavesNoResidue asserts the two observable
// halves of in-workdir staging: during the fetch, a .vibed-stage-* dir exists
// INSIDE the workdir (observed by the tarball server at request time — being
// inside the workdir means the later adoption is a same-filesystem rename,
// which we cannot watch directly), and after a successful inject the files
// are in place with no .vibed-stage-* residue left behind.
func TestInjectStagesInsideWorkdirAndLeavesNoResidue(t *testing.T) {
	workdir := t.TempDir()
	tarball := tarballFromMap(t, map[string]string{"hello.txt": "from-tarball"})

	var sawStage atomic.Bool
	tarServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if len(stageEntries(workdir)) > 0 {
			sawStage.Store(true)
		}
		_, _ = w.Write(tarball)
	}))
	defer tarServer.Close()

	_, c := sourceURLAgent(t, workdir)
	if _, err := c.Inject(context.Background(), InjectRequest{
		SourceURL: tarServer.URL,
		Command:   []string{"sh", "-c", "sleep 5"},
	}); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	if !sawStage.Load() {
		t.Error("no .vibed-stage-* dir was visible inside the workdir during the fetch")
	}
	if got, err := os.ReadFile(filepath.Join(workdir, "hello.txt")); err != nil || string(got) != "from-tarball" {
		t.Fatalf("hello.txt = %q (err %v), want %q", got, err, "from-tarball")
	}
	if left := stageEntries(workdir); len(left) != 0 {
		t.Errorf("stage residue left in workdir after inject: %v", left)
	}
}

// TestReinjectWithSourceURLReplacesSource injects twice from SourceURL: the
// second inject must fully replace the first's files (staging inside the
// workdir must not confuse the reset/swap) and leave no stage residue.
func TestReinjectWithSourceURLReplacesSource(t *testing.T) {
	workdir := t.TempDir()
	srvFor := func(files map[string]string) *httptest.Server {
		b := tarballFromMap(t, files)
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(b)
		}))
		t.Cleanup(s.Close)
		return s
	}
	firstSrv := srvFor(map[string]string{"first.txt": "1"})
	secondSrv := srvFor(map[string]string{"second.txt": "2"})

	_, c := sourceURLAgent(t, workdir)
	for _, u := range []string{firstSrv.URL, secondSrv.URL} {
		if _, err := c.Inject(context.Background(), InjectRequest{
			SourceURL: u,
			Command:   []string{"sh", "-c", "sleep 5"},
		}); err != nil {
			t.Fatalf("Inject %s: %v", u, err)
		}
	}

	if _, err := os.Stat(filepath.Join(workdir, "second.txt")); err != nil {
		t.Fatalf("second inject's file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "first.txt")); !os.IsNotExist(err) {
		t.Errorf("first inject's file should be gone, stat err = %v", err)
	}
	if left := stageEntries(workdir); len(left) != 0 {
		t.Errorf("stage residue after re-inject: %v", left)
	}
}

// TestInjectFailedFetchLeavesWorkdirIntact: a fetch that dies mid-extraction
// must remove its staging dir and must NOT disturb the previously deployed
// workdir contents (the fetch fails before the reset/swap section).
func TestInjectFailedFetchLeavesWorkdirIntact(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "existing.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("seed workdir: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not a gzip stream"))
	}))
	defer bad.Close()

	_, c := sourceURLAgent(t, workdir)
	if _, err := c.Inject(context.Background(), InjectRequest{
		SourceURL: bad.URL,
		Command:   []string{"sh", "-c", "sleep 5"},
	}); err == nil {
		t.Fatal("expected inject to fail on a bad tarball")
	}

	if got, err := os.ReadFile(filepath.Join(workdir, "existing.txt")); err != nil || string(got) != "keep" {
		t.Fatalf("existing workdir content disturbed: %q (err %v)", got, err)
	}
	if left := stageEntries(workdir); len(left) != 0 {
		t.Errorf("stage residue after failed fetch: %v", left)
	}
}

// TestInjectCleansStaleStageDirs: a .vibed-stage-* dir left by a crashed agent
// (not registered in a.stages) is ordinary residue and must be cleared by the
// next successful inject's workdir reset.
func TestInjectCleansStaleStageDirs(t *testing.T) {
	workdir := t.TempDir()
	stale := filepath.Join(workdir, ".vibed-stage-stale")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mk stale stage dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, "junk"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed stale stage dir: %v", err)
	}

	tarball := tarballFromMap(t, map[string]string{"app.txt": "v"})
	tarServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer tarServer.Close()

	_, c := sourceURLAgent(t, workdir)
	if _, err := c.Inject(context.Background(), InjectRequest{
		SourceURL: tarServer.URL,
		Command:   []string{"sh", "-c", "sleep 5"},
	}); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	if left := stageEntries(workdir); len(left) != 0 {
		t.Errorf("stale stage dir survived inject: %v", left)
	}
	if _, err := os.Stat(filepath.Join(workdir, "app.txt")); err != nil {
		t.Fatalf("injected file missing: %v", err)
	}
}

func TestInjectRejectsBothFilesAndSourceURL(t *testing.T) {
	a := New(Config{Workdir: t.TempDir(), AppPort: 8080, StopGrace: 100 * time.Millisecond})
	t.Cleanup(func() { a.stopProcess() })
	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.Inject(context.Background(), InjectRequest{
		Files:     map[string]string{"a": "b"},
		SourceURL: "http://example/x.tgz",
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

// silence linter if io is otherwise unused in the file.
var _ = io.Discard
