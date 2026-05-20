package tarball

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibed-project/vibeD/internal/config"
)

func TestBlobHandlerServesStoredTarball(t *testing.T) {
	dir := t.TempDir()
	cfg := config.TarballConfig{
		Backend: "served",
		Served:  config.ServedTarballConfig{BasePath: dir, PublicBaseURL: "http://x"},
	}
	store, _ := newServedStore(cfg.Served)
	if _, err := store.Put(context.Background(), "abc", strings.NewReader("TARBALL-BYTES")); err != nil {
		t.Fatal(err)
	}

	h, err := NewBlobHandler(cfg)
	if err != nil {
		t.Fatalf("NewBlobHandler: %v", err)
	}
	if h == nil {
		t.Fatal("expected a handler for served backend")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, BlobPathPrefix+"abc.tar.gz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "TARBALL-BYTES" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestBlobHandlerRejectsNonTarball(t *testing.T) {
	dir := t.TempDir()
	cfg := config.TarballConfig{Backend: "served", Served: config.ServedTarballConfig{BasePath: dir, PublicBaseURL: "http://x"}}
	h, _ := NewBlobHandler(cfg)

	// A path that isn't *.tar.gz must 404 (no directory listing, no other files).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, BlobPathPrefix, nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for non-tarball path", rec.Code)
	}
}

func TestBlobHandlerNilForS3(t *testing.T) {
	h, err := NewBlobHandler(config.TarballConfig{Backend: "s3"})
	if err != nil {
		t.Fatalf("NewBlobHandler: %v", err)
	}
	if h != nil {
		t.Error("s3 backend should not serve blobs locally (agent pulls from object storage)")
	}
}
