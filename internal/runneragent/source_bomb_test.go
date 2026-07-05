package runneragent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// gzipBomb builds a gzipped tarball containing entryCount entries, each of
// bytesPerEntry uncompressed zero bytes. Highly compressible zeros make a
// tiny compressed payload that expands enormously — a classic decompression
// bomb. The COMPRESSED result stays well under MaxTarballBytes so only the
// uncompressed caps can catch it.
func gzipBomb(t *testing.T, entryCount int, bytesPerEntry int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	zeros := make([]byte, 32*1024)
	for i := 0; i < entryCount; i++ {
		if err := tw.WriteHeader(&tar.Header{
			Name:     "bomb/file-" + string(rune('a'+i%26)) + "-" + itoa(i),
			Mode:     0o644,
			Size:     bytesPerEntry,
			Typeflag: tar.TypeReg,
			ModTime:  time.Unix(0, 0),
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		remaining := bytesPerEntry
		for remaining > 0 {
			n := int64(len(zeros))
			if n > remaining {
				n = remaining
			}
			if _, err := tw.Write(zeros[:n]); err != nil {
				t.Fatalf("write body: %v", err)
			}
			remaining -= n
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

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func serveBytes(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
}

// TestFetchRejectsPerEntryBomb: a single entry larger than MaxEntryBytes is
// rejected even though the compressed archive is tiny.
func TestFetchRejectsPerEntryBomb(t *testing.T) {
	bomb := gzipBomb(t, 1, MaxEntryBytes+1024)
	if len(bomb) >= MaxTarballBytes {
		t.Fatalf("compressed bomb (%d bytes) should be far under the compressed cap", len(bomb))
	}
	srv := serveBytes(t, bomb)
	defer srv.Close()

	err := NewSourceFetcher().Fetch(context.Background(), srv.URL, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "uncompressed size limit") {
		t.Fatalf("expected per-entry uncompressed-limit error, got %v", err)
	}
}

// TestFetchRejectsCumulativeBomb: many entries each within the per-entry cap
// but together exceeding the cumulative uncompressed budget are rejected.
func TestFetchRejectsCumulativeBomb(t *testing.T) {
	// 8 entries * 80 MB = 640 MB > 500 MB cumulative cap, each < 100 MB per-entry cap.
	bomb := gzipBomb(t, 8, 80*1024*1024)
	if len(bomb) >= MaxTarballBytes {
		t.Fatalf("compressed bomb (%d bytes) should be under the compressed cap", len(bomb))
	}
	srv := serveBytes(t, bomb)
	defer srv.Close()

	err := NewSourceFetcher().Fetch(context.Background(), srv.URL, t.TempDir())
	if err == nil {
		t.Fatal("expected cumulative uncompressed-limit error, got nil")
	}
	if !strings.Contains(err.Error(), "uncompressed") {
		t.Fatalf("expected uncompressed-limit error, got %v", err)
	}
}

// TestFetchRejectsTooManyEntries: an archive with more than MaxEntries entries
// is rejected (inode-exhaustion defense), even if tiny.
func TestFetchRejectsTooManyEntries(t *testing.T) {
	bomb := gzipBomb(t, MaxEntries+1, 0)
	srv := serveBytes(t, bomb)
	defer srv.Close()

	err := NewSourceFetcher().Fetch(context.Background(), srv.URL, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("expected too-many-entries error, got %v", err)
	}
}

// TestFetchAllowsNormalArchive: a normal small archive still extracts fine —
// the caps don't regress the happy path.
func TestFetchAllowsNormalArchive(t *testing.T) {
	tarball := tarballFromMap(t, map[string]string{
		"app.py":    "print('hi')\n",
		"README.md": "# hello\n",
	})
	srv := serveBytes(t, tarball)
	defer srv.Close()

	if err := NewSourceFetcher().Fetch(context.Background(), srv.URL, t.TempDir()); err != nil {
		t.Fatalf("normal archive should extract, got %v", err)
	}
}
