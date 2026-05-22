package runneragent

import (
	"bytes"
	"sync"
)

// ringBuffer is a fixed-capacity, line-oriented log buffer. It implements
// io.Writer so it can be wired directly into a process's stdout/stderr, and
// keeps only the most recent `capacity` complete lines. It is safe for
// concurrent use.
type ringBuffer struct {
	mu       sync.Mutex
	capacity int
	lines    []string // most recent at the end
	partial  []byte   // bytes since the last newline
}

func newRingBuffer(capacity int) *ringBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &ringBuffer{capacity: capacity}
}

// Write splits incoming bytes on newlines, appending complete lines to the
// buffer and holding any trailing partial line until the next Write.
func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		r.appendLine(string(r.partial[:i]))
		r.partial = r.partial[i+1:]
	}
	// Avoid an unbounded partial line if a process never emits a newline.
	if len(r.partial) > 64*1024 {
		r.appendLine(string(r.partial))
		r.partial = r.partial[:0]
	}
	return len(p), nil
}

// appendLine adds a line, evicting the oldest if at capacity. Caller holds mu.
func (r *ringBuffer) appendLine(line string) {
	if len(r.lines) >= r.capacity {
		// Drop the oldest. Copy to keep the backing array bounded.
		r.lines = append(r.lines[:0], r.lines[1:]...)
	}
	r.lines = append(r.lines, line)
}

// snapshot returns up to the last n captured lines (all of them when n <= 0),
// including any trailing partial line.
func (r *ringBuffer) snapshot(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, len(r.lines))
	copy(out, r.lines)
	if len(r.partial) > 0 {
		out = append(out, string(r.partial))
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// reset clears the buffer — used when a new inject replaces the user process.
func (r *ringBuffer) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = r.lines[:0]
	r.partial = r.partial[:0]
}
