package runneragent

import (
	"bytes"
	"sync"
)

// ringBuffer is a fixed-capacity, line-oriented log buffer. It implements
// io.Writer so it can be wired directly into a process's stdout/stderr, and
// keeps only the most recent `capacity` complete lines. It is safe for
// concurrent use.
//
// Lines are stored in a true circular buffer: appending at capacity overwrites
// the oldest slot in O(1). The previous implementation shifted the whole slice
// left on every line once full (O(n) per line → O(n²) for a chatty process);
// the ring makes steady-state logging O(1) per line regardless of capacity.
type ringBuffer struct {
	mu       sync.Mutex
	capacity int
	buf      []string // circular; len==capacity once filled
	start    int      // index of the oldest line
	count    int      // number of valid lines (<= capacity)
	partial  []byte   // bytes since the last newline
}

func newRingBuffer(capacity int) *ringBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &ringBuffer{capacity: capacity, buf: make([]string, capacity)}
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

// appendLine adds a line in O(1), overwriting the oldest slot when at capacity.
// Caller holds mu.
func (r *ringBuffer) appendLine(line string) {
	if r.count < r.capacity {
		r.buf[(r.start+r.count)%r.capacity] = line
		r.count++
		return
	}
	// Full: overwrite the oldest and advance start.
	r.buf[r.start] = line
	r.start = (r.start + 1) % r.capacity
}

// snapshot returns up to the last n captured lines (all of them when n <= 0),
// including any trailing partial line.
func (r *ringBuffer) snapshot(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, 0, r.count+1)
	for i := 0; i < r.count; i++ {
		out = append(out, r.buf[(r.start+i)%r.capacity])
	}
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
	r.start = 0
	r.count = 0
	r.partial = r.partial[:0]
}
