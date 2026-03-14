// Package events provides an in-memory publish/subscribe event bus for
// streaming artifact lifecycle events to SSE clients.
package events

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// EventType identifies the kind of lifecycle event.
type EventType string

const (
	// ArtifactStatusChanged is published when an artifact transitions between
	// lifecycle states (pending, building, deploying, running, failed).
	ArtifactStatusChanged EventType = "artifact.status_changed"

	// ArtifactDeleted is published when an artifact is removed.
	ArtifactDeleted EventType = "artifact.deleted"
)

// Event represents a single lifecycle event for an artifact.
type Event struct {
	ID         string    `json:"id"`
	Type       EventType `json:"type"`
	ArtifactID string    `json:"artifact_id"`
	Status     string    `json:"status,omitempty"`
	Error      string    `json:"error,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// subscriberBufferSize is the channel buffer for each subscriber.
// Events are dropped for slow consumers to prevent blocking publishers.
const subscriberBufferSize = 64

// EventBus fans out events to all active subscribers.
// It is safe for concurrent use.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[uint64]chan Event
	nextSubID   uint64
	eventSeq    atomic.Uint64
}

// NewEventBus creates a new EventBus ready for use.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[uint64]chan Event),
	}
}

// Publish sends an event to all active subscribers.
// It assigns a monotonically increasing ID to the event.
// Non-blocking: events are dropped for subscribers whose buffers are full.
func (b *EventBus) Publish(event Event) {
	seq := b.eventSeq.Add(1)
	event.ID = fmt.Sprintf("%d", seq)

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			// Slow consumer — drop event to avoid blocking the publisher.
		}
	}
}

// Subscribe returns a channel that receives published events and an
// unsubscribe function. The channel is closed when the context is
// cancelled or unsubscribe is called.
func (b *EventBus) Subscribe(ctx context.Context) (<-chan Event, func()) {
	ch := make(chan Event, subscriberBufferSize)

	b.mu.Lock()
	id := b.nextSubID
	b.nextSubID++
	b.subscribers[id] = ch
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, id)
			b.mu.Unlock()
			close(ch)
		})
	}

	// Auto-unsubscribe on context cancellation.
	go func() {
		<-ctx.Done()
		unsub()
	}()

	return ch, unsub
}
