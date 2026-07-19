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
// Storage is a true circular buffer: `buf` is a fixed slice of len==capacity,
// `start` indexes the oldest line and `count` the number of live lines. This
// makes eviction O(1) (overwrite one slot, advance start) instead of the O(n)
// shift a growing slice would need per line on a high-volume stream.
type ringBuffer struct {
	mu       sync.Mutex
	capacity int
	buf      []string // circular; len == capacity, oldest at start
	start    int      // index of the oldest live line
	count    int      // number of live lines (0..capacity)
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

// appendLine adds a line, evicting the oldest in O(1) when at capacity. Caller
// holds mu.
func (r *ringBuffer) appendLine(line string) {
	end := (r.start + r.count) % r.capacity
	r.buf[end] = line
	if r.count < r.capacity {
		r.count++
		return
	}
	// Full: the write above landed on the oldest slot (end == start), so that
	// slot is now the newest line; advance start to the next-oldest.
	r.start = (r.start + 1) % r.capacity
}

// snapshot returns up to the last n captured lines (all of them when n <= 0),
// including any trailing partial line. It copies only the tail it returns, not
// the whole buffer.
func (r *ringBuffer) snapshot(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	hasPartial := len(r.partial) > 0
	total := r.count
	if hasPartial {
		total++
	}
	take := total
	if n > 0 && n < take {
		take = n
	}
	if take <= 0 {
		return []string{}
	}

	// The logical order is [oldest line ... newest line, partial?]; we return
	// the last `take` of that sequence. When a partial is present it is the very
	// last element, so it consumes one slot and the rest come from committed
	// lines.
	commitTake := take
	if hasPartial {
		commitTake = take - 1
	}
	skip := r.count - commitTake // number of oldest committed lines to drop

	out := make([]string, 0, take)
	for i := 0; i < commitTake; i++ {
		out = append(out, r.buf[(r.start+skip+i)%r.capacity])
	}
	if hasPartial {
		out = append(out, string(r.partial))
	}
	return out
}

// reset clears the buffer — used when a new inject replaces the user process.
func (r *ringBuffer) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Drop references so evicted log lines can be GC'd rather than pinned by the
	// backing array until overwritten.
	for i := range r.buf {
		r.buf[i] = ""
	}
	r.start = 0
	r.count = 0
	r.partial = r.partial[:0]
}
