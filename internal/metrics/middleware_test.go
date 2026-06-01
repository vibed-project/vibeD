package metrics

import (
	"bufio"
	"net"
	"net/http"
	"testing"
)

// fakeWriter implements the union of optional ResponseWriter interfaces so
// we can verify the wrapper forwards them all. Each method increments a
// counter so tests can assert it was actually called.
type fakeWriter struct {
	header   http.Header
	wrote    bool
	status   int
	flushes  int
	hijacks  int
	hijackOK bool
}

func newFakeWriter(hijack bool) *fakeWriter {
	return &fakeWriter{header: http.Header{}, hijackOK: hijack}
}

func (f *fakeWriter) Header() http.Header             { return f.header }
func (f *fakeWriter) WriteHeader(code int)            { f.wrote = true; f.status = code }
func (f *fakeWriter) Write(b []byte) (int, error)     { return len(b), nil }
func (f *fakeWriter) Flush()                          { f.flushes++ }
func (f *fakeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	f.hijacks++
	if f.hijackOK {
		return nil, nil, nil
	}
	return nil, nil, http.ErrNotSupported
}

// TestResponseWriterForwardsFlusher pins the SSE-and-streaming contract:
// the metrics wrapper MUST expose http.Flusher so the MCP SDK's server-side
// streamable HTTP can push response headers immediately. Without this, the
// SSE GET hangs on the client side waiting for headers that never arrive,
// which manifested as a 10-minute test-binary timeout in e2e CI.
func TestResponseWriterForwardsFlusher(t *testing.T) {
	inner := newFakeWriter(true)
	rw := &responseWriter{ResponseWriter: inner}

	f, ok := http.ResponseWriter(rw).(http.Flusher)
	if !ok {
		t.Fatal("metrics.responseWriter does not satisfy http.Flusher")
	}
	f.Flush()
	if inner.flushes != 1 {
		t.Errorf("Flush() = %d underlying flushes, want 1", inner.flushes)
	}
}

// TestResponseWriterForwardsHijacker covers the WebSocket / hijacker path
// (logs streaming + any future bidirectional transport). Same shape as the
// Flusher test: assert the wrapper is transparent.
func TestResponseWriterForwardsHijacker(t *testing.T) {
	inner := newFakeWriter(true)
	rw := &responseWriter{ResponseWriter: inner}

	h, ok := http.ResponseWriter(rw).(http.Hijacker)
	if !ok {
		t.Fatal("metrics.responseWriter does not satisfy http.Hijacker")
	}
	if _, _, err := h.Hijack(); err != nil {
		t.Errorf("Hijack() returned %v, want nil from the underlying writer", err)
	}
	if inner.hijacks != 1 {
		t.Errorf("Hijack() = %d underlying calls, want 1", inner.hijacks)
	}
}

// TestResponseWriterCapturesStatus pins the original purpose of the
// wrapper (recording the response code into the metrics path).
func TestResponseWriterCapturesStatus(t *testing.T) {
	inner := newFakeWriter(false)
	rw := &responseWriter{ResponseWriter: inner, statusCode: http.StatusOK}

	rw.WriteHeader(http.StatusTeapot)
	if rw.statusCode != http.StatusTeapot {
		t.Errorf("captured status = %d, want %d", rw.statusCode, http.StatusTeapot)
	}
	if inner.status != http.StatusTeapot || !inner.wrote {
		t.Errorf("inner writer not called: wrote=%v status=%d", inner.wrote, inner.status)
	}
}
