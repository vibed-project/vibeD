package classifier

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"
	"time"
)

// gzipBomb builds a highly compressible gzipped tarball: entryCount entries of
// bytesPerEntry zero bytes each. The compressed output is tiny; the
// uncompressed expansion is entryCount*bytesPerEntry.
func gzipBomb(t *testing.T, entryCount int, bytesPerEntry int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	zeros := make([]byte, 32*1024)
	for i := 0; i < entryCount; i++ {
		if err := tw.WriteHeader(&tar.Header{
			Name:     "data/blob-" + itoa(i) + ".bin",
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

// TestClassifyRejectsDecompressionBomb: a small gzip that expands past the
// uncompressed budget is rejected rather than scanned to completion.
func TestClassifyRejectsDecompressionBomb(t *testing.T) {
	// 8 * 80 MB = 640 MB uncompressed > 500 MB cap. Compressed stays tiny.
	bomb := gzipBomb(t, 8, 80*1024*1024)
	if len(bomb) >= MaxTarballBytes {
		t.Fatalf("compressed bomb (%d bytes) should be under the compressed cap", len(bomb))
	}
	_, err := Classifier{}.Classify(context.Background(), bytes.NewReader(bomb))
	if err == nil {
		t.Fatal("expected an error classifying a decompression bomb, got nil")
	}
	if !strings.Contains(err.Error(), "uncompressed") && !strings.Contains(err.Error(), "read tarball") {
		t.Fatalf("expected uncompressed-limit error, got %v", err)
	}
}

// TestClassifyRejectsTooManyEntries: an archive exceeding MaxEntries is
// rejected even when tiny.
func TestClassifyRejectsTooManyEntries(t *testing.T) {
	bomb := gzipBomb(t, MaxEntries+1, 0)
	_, err := Classifier{}.Classify(context.Background(), bytes.NewReader(bomb))
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("expected too-many-entries error, got %v", err)
	}
}

// TestClassifyNormalArchiveStillWorks: the caps don't regress normal
// classification.
func TestClassifyNormalArchiveStillWorks(t *testing.T) {
	d := classify(t, map[string]string{
		"index.html": "<h1>hi</h1>",
		"styles.css": "body{}",
	})
	if d.Rule != RuleStaticOnly {
		t.Errorf("normal static archive: Rule = %d, want %d", d.Rule, RuleStaticOnly)
	}
}
