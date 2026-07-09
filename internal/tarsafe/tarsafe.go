// Package tarsafe bounds the work an extractor performs on an untrusted gzipped
// tarball, defeating decompression bombs: a small compressed input (well under
// a compressed-size cap) can inflate to many gigabytes, exhausting a sandbox's
// disk/inodes. Callers wrap the decompressed stream in a LimitedReader that
// aborts once cumulative output exceeds MaxUncompressedBytes, and enforce
// MaxEntries in their tar loop.
package tarsafe

import (
	"errors"
	"io"
)

const (
	// MaxUncompressedBytes caps the total inflated size of a tarball. Source
	// tarballs are capped at 50 MB compressed; this allows ~10x expansion, far
	// under the tens of GB a bomb would otherwise write.
	MaxUncompressedBytes = 512 * 1024 * 1024

	// MaxEntries caps the number of tar entries, bounding the inode exhaustion
	// (and loop-count) amplification of an archive packed with many tiny or
	// empty files that individually stay under the byte budget.
	MaxEntries = 100_000
)

// ErrTooLarge is returned by a LimitedReader once its uncompressed byte budget
// is exceeded.
var ErrTooLarge = errors.New("tarsafe: uncompressed data exceeds limit")

// ErrTooManyEntries is a suggested sentinel for callers that hit MaxEntries.
var ErrTooManyEntries = errors.New("tarsafe: too many tar entries")

// LimitedReader reads from R but returns ErrTooLarge once more than its initial
// budget has been read in total. Unlike io.LimitReader it fails hard rather than
// reporting a silent EOF, so an overrun aborts extraction instead of yielding a
// truncated tree that looks complete.
type LimitedReader struct {
	R io.Reader
	n int64 // bytes remaining before overrun; goes negative on the read that overflows
}

// NewLimitedReader caps cumulative reads from r at max bytes.
func NewLimitedReader(r io.Reader, max int64) *LimitedReader {
	return &LimitedReader{R: r, n: max}
}

func (l *LimitedReader) Read(p []byte) (int, error) {
	if l.n < 0 {
		return 0, ErrTooLarge
	}
	n, err := l.R.Read(p)
	l.n -= int64(n)
	if l.n < 0 {
		// This read pushed the total past the budget; surface the bytes read so
		// far alongside the hard error so the caller stops immediately.
		return n, ErrTooLarge
	}
	return n, err
}
