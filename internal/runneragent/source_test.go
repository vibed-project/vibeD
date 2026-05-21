package runneragent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	if err := NewSourceFetcher().Fetch(context.Background(), srv.URL, dir); err != nil {
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

	err := NewSourceFetcher().Fetch(context.Background(), srv.URL, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "illegal tar entry") {
		t.Fatalf("expected illegal-tar-entry error, got %v", err)
	}
}

func TestSourceFetcherRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	err := NewSourceFetcher().Fetch(context.Background(), srv.URL, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 surfaced, got %v", err)
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
