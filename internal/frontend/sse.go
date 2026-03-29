package frontend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/metrics"
)

const maxSSEConnections = 100

// handleSSE returns an HTTP handler that streams artifact lifecycle events
// using Server-Sent Events (SSE). Each connected client receives all events
// published to the EventBus for the duration of the connection.
//
// The handler sends a heartbeat comment every 30 seconds to keep the
// connection alive through proxies and load balancers.
func handleSSE(bus *events.EventBus, m *metrics.Metrics) http.HandlerFunc {
	var activeConns atomic.Int64

	return func(w http.ResponseWriter, r *http.Request) {
		if activeConns.Load() >= maxSSEConnections {
			http.Error(w, "too many SSE connections", http.StatusServiceUnavailable)
			return
		}
		activeConns.Add(1)
		defer activeConns.Add(-1)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

		m.SSEConnectionsActive.Inc()
		defer m.SSEConnectionsActive.Dec()

		// Determine the authenticated user for event filtering.
		// When auth is disabled (empty userID), all events are sent.
		userID := vibedauth.UserIDFromContext(r.Context())
		isAdmin := vibedauth.IsAdmin(r.Context())

		ch, unsub := bus.Subscribe(r.Context())
		defer unsub()

		// Heartbeat every 30 seconds to keep connection alive.
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case event, ok := <-ch:
				if !ok {
					return
				}
				// Filter events by ownership: admins and unauthenticated (auth disabled) see all.
				if userID != "" && !isAdmin && event.OwnerID != "" && event.OwnerID != userID {
					continue
				}
				data, _ := json.Marshal(event)
				fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data)
				flusher.Flush()

			case <-ticker.C:
				fmt.Fprintf(w, ": heartbeat\n\n")
				flusher.Flush()

			case <-r.Context().Done():
				return
			}
		}
	}
}
