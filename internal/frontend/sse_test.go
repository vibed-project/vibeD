package frontend

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/metrics"
)

// TestHandleSSEWritesPrecomputedPayload asserts the SSE handler streams the
// exact bytes Publish pre-marshaled, rather than re-encoding per connection.
func TestHandleSSEWritesPrecomputedPayload(t *testing.T) {
	bus := events.NewEventBus()
	srv := httptest.NewServer(handleSSE(bus, metrics.New()))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Parallel subscription: captures the published events (with their
	// Publish-assigned IDs and cached payloads) for comparison below.
	capture, unsubCapture := bus.Subscribe(ctx)
	defer unsubCapture()

	// The handler only subscribes once the request reaches it, so republish
	// until the first event makes it through the stream.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			bus.Publish(events.Event{
				Type:       events.ArtifactStatusChanged,
				ArtifactID: "app-1",
				Name:       "app-1",
				OwnerID:    "alice",
				Status:     "Ready",
				Phase:      "Ready",
				URL:        "https://app-1.example.com",
				Template:   "node-24",
				Timestamp:  time.Unix(1700000000, 0).UTC(),
			})
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	defer resp.Body.Close()

	// Read the first full SSE frame (id + data lines).
	var id, data string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() && (id == "" || data == "") {
		line := scanner.Text()
		if v, ok := strings.CutPrefix(line, "id: "); ok && id == "" {
			id = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok && data == "" {
			data = v
		}
	}
	if id == "" || data == "" {
		t.Fatalf("incomplete SSE frame: id=%q data=%q (scan err: %v)", id, data, scanner.Err())
	}

	// Match the frame to the captured event by ID and require byte equality
	// with the pre-marshaled payload.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-capture:
			if ev.ID != id {
				continue // earlier publish the handler missed
			}
			if want := string(ev.Payload()); data != want {
				t.Fatalf("data line = %s, want pre-marshaled payload %s", data, want)
			}
			return
		case <-deadline:
			t.Fatalf("never captured event with ID %s", id)
		}
	}
}
