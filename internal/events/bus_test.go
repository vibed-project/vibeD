package events

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestPublishToSubscriber(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, unsub := bus.Subscribe(ctx)
	defer unsub()

	bus.Publish(Event{
		Type:       ArtifactStatusChanged,
		ArtifactID: "art-1",
		Status:     "building",
		Timestamp:  time.Now(),
	})

	select {
	case ev := <-ch:
		if ev.ArtifactID != "art-1" {
			t.Fatalf("expected artifact_id art-1, got %s", ev.ArtifactID)
		}
		if ev.Status != "building" {
			t.Fatalf("expected status building, got %s", ev.Status)
		}
		if ev.ID == "" {
			t.Fatal("expected non-empty event ID")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestMultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numSubs = 3
	channels := make([]<-chan Event, numSubs)
	unsubs := make([]func(), numSubs)
	for i := 0; i < numSubs; i++ {
		channels[i], unsubs[i] = bus.Subscribe(ctx)
		defer unsubs[i]()
	}

	bus.Publish(Event{
		Type:       ArtifactStatusChanged,
		ArtifactID: "art-2",
		Status:     "running",
		Timestamp:  time.Now(),
	})

	for i, ch := range channels {
		select {
		case ev := <-ch:
			if ev.ArtifactID != "art-2" {
				t.Fatalf("subscriber %d: expected artifact_id art-2, got %s", i, ev.ArtifactID)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, unsub := bus.Subscribe(ctx)
	unsub()

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Fatal("expected channel to be closed after unsubscribe")
	}

	// Publishing after unsubscribe should not panic.
	bus.Publish(Event{
		Type:       ArtifactStatusChanged,
		ArtifactID: "art-3",
		Timestamp:  time.Now(),
	})
}

func TestContextCancelStopsDelivery(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())

	ch, _ := bus.Subscribe(ctx)
	cancel()

	// Give the goroutine time to process cancellation.
	time.Sleep(50 * time.Millisecond)

	_, ok := <-ch
	if ok {
		t.Fatal("expected channel to be closed after context cancel")
	}
}

func TestSlowConsumerDoesNotBlock(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, unsub := bus.Subscribe(ctx) // subscribe but never read
	defer unsub()

	// Fill the buffer + extras — should not block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBufferSize+10; i++ {
			bus.Publish(Event{
				Type:       ArtifactStatusChanged,
				ArtifactID: "art-slow",
				Timestamp:  time.Now(),
			})
		}
		close(done)
	}()

	select {
	case <-done:
		// Success — publish did not block.
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on slow consumer")
	}
}

func TestNoSubscribersNoPanic(t *testing.T) {
	bus := NewEventBus()

	// Should not panic.
	bus.Publish(Event{
		Type:       ArtifactDeleted,
		ArtifactID: "art-none",
		Timestamp:  time.Now(),
	})
}

func TestMonotonicEventIDs(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, unsub := bus.Subscribe(ctx)
	defer unsub()

	const n = 10
	for i := 0; i < n; i++ {
		bus.Publish(Event{
			Type:       ArtifactStatusChanged,
			ArtifactID: "art-mono",
			Timestamp:  time.Now(),
		})
	}

	var lastSeq uint64
	for i := 0; i < n; i++ {
		select {
		case ev := <-ch:
			seq, err := strconv.ParseUint(ev.ID, 10, 64)
			if err != nil {
				t.Fatalf("event ID %q is not a number: %v", ev.ID, err)
			}
			if seq <= lastSeq && lastSeq != 0 {
				t.Fatalf("event IDs not monotonic: %d <= %d", seq, lastSeq)
			}
			lastSeq = seq
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

func TestPublishPrecomputesPayload(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, unsub := bus.Subscribe(ctx)
	defer unsub()

	bus.Publish(Event{
		Type:       ArtifactStatusChanged,
		ArtifactID: "art-payload",
		Name:       "art-payload",
		OwnerID:    "alice",
		Status:     "Ready",
		Phase:      "Ready",
		URL:        "https://art-payload.example.com",
		Template:   "node-24",
		Timestamp:  time.Now(),
	})

	select {
	case ev := <-ch:
		// Publish must pre-marshal so subscribers never re-encode.
		if ev.payload == nil {
			t.Fatal("expected Publish to pre-marshal the event payload")
		}
		want, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !bytes.Equal(ev.Payload(), want) {
			t.Fatalf("payload mismatch:\n got: %s\nwant: %s", ev.Payload(), want)
		}
		// The enriched fields must round-trip through the cached bytes.
		var decoded Event
		if err := json.Unmarshal(ev.Payload(), &decoded); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if decoded.Name != "art-payload" || decoded.Phase != "Ready" ||
			decoded.URL != "https://art-payload.example.com" || decoded.Template != "node-24" {
			t.Fatalf("enriched fields lost in payload: %+v", decoded)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestPayloadFallbackWithoutPublish(t *testing.T) {
	// Events built by hand (never routed through Publish) still encode.
	ev := Event{Type: ArtifactDeleted, ArtifactID: "art-fallback"}
	var decoded Event
	if err := json.Unmarshal(ev.Payload(), &decoded); err != nil {
		t.Fatalf("unmarshal fallback payload: %v", err)
	}
	if decoded.ArtifactID != "art-fallback" {
		t.Fatalf("expected artifact_id art-fallback, got %s", decoded.ArtifactID)
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Start 5 subscribers concurrently.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, unsub := bus.Subscribe(ctx)
			defer unsub()
			// Drain a few events.
			for j := 0; j < 3; j++ {
				select {
				case <-ch:
				case <-time.After(time.Second):
				}
			}
		}()
	}

	// Publish 20 events concurrently.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Publish(Event{
				Type:       ArtifactStatusChanged,
				ArtifactID: "art-conc",
				Timestamp:  time.Now(),
			})
		}()
	}

	wg.Wait()
}
