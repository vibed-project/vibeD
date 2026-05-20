package tarball

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibed-project/vibeD/internal/config"
)

func TestServedStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := newServedStore(config.ServedTarballConfig{
		BasePath:      dir,
		PublicBaseURL: "http://vibed.vibed-system.svc.cluster.local:8080/",
	})
	if err != nil {
		t.Fatalf("newServedStore: %v", err)
	}

	body := []byte("gzip-tarball-bytes")
	url, err := s.Put(context.Background(), "app123", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// URL shape: trailing slash on PublicBaseURL is trimmed, prefix + object appended.
	want := "http://vibed.vibed-system.svc.cluster.local:8080" + BlobPathPrefix + "app123.tar.gz"
	if url != want {
		t.Errorf("url = %q, want %q", url, want)
	}

	// Blob landed on disk with the canonical name.
	got, err := os.ReadFile(filepath.Join(dir, "app123.tar.gz"))
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("blob bytes mismatch")
	}
}

func TestServedStoreOverwriteOnRedeploy(t *testing.T) {
	dir := t.TempDir()
	s, _ := newServedStore(config.ServedTarballConfig{BasePath: dir, PublicBaseURL: "http://x"})

	if _, err := s.Put(context.Background(), "app", strings.NewReader("v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(context.Background(), "app", strings.NewReader("v2-longer")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "app.tar.gz"))
	if string(got) != "v2-longer" {
		t.Errorf("redeploy did not overwrite: got %q", got)
	}

	// No leftover temp files from the atomic-rename write.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".put-") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
}

func TestServedStoreDeleteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s, _ := newServedStore(config.ServedTarballConfig{BasePath: dir, PublicBaseURL: "http://x"})
	_, _ = s.Put(context.Background(), "app", strings.NewReader("data"))

	if err := s.Delete(context.Background(), "app"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := s.Delete(context.Background(), "app"); err != nil {
		t.Fatalf("second delete (already gone) should be a no-op: %v", err)
	}
}

func TestServedStoreRequiresConfig(t *testing.T) {
	if _, err := newServedStore(config.ServedTarballConfig{PublicBaseURL: "http://x"}); err == nil {
		t.Error("expected error when basePath is empty")
	}
	if _, err := newServedStore(config.ServedTarballConfig{BasePath: t.TempDir()}); err == nil {
		t.Error("expected error when publicBaseURL is empty")
	}
}

func TestNewFactory(t *testing.T) {
	dir := t.TempDir()
	served, err := New(config.TarballConfig{
		Backend: "served",
		Served:  config.ServedTarballConfig{BasePath: dir, PublicBaseURL: "http://x"},
	})
	if err != nil {
		t.Fatalf("served factory: %v", err)
	}
	if _, ok := served.(*servedStore); !ok {
		t.Errorf("expected *servedStore, got %T", served)
	}

	// Empty backend defaults to served.
	def, err := New(config.TarballConfig{
		Served: config.ServedTarballConfig{BasePath: dir, PublicBaseURL: "http://x"},
	})
	if err != nil || def == nil {
		t.Fatalf("default backend should be served: %v", err)
	}

	s3, err := New(config.TarballConfig{
		Backend: "s3",
		S3:      config.S3TarballConfig{Bucket: "vibed-sources", Region: "us-east-1"},
	})
	if err != nil {
		t.Fatalf("s3 factory: %v", err)
	}
	if _, ok := s3.(*s3Store); !ok {
		t.Errorf("expected *s3Store, got %T", s3)
	}

	if _, err := New(config.TarballConfig{Backend: "bogus"}); err == nil {
		t.Error("expected error for unknown backend")
	}
}

// compile-time: stores satisfy the interface.
var (
	_ Store          = (*servedStore)(nil)
	_ Store          = (*s3Store)(nil)
	_ io.Reader      = (*bytes.Reader)(nil)
)
