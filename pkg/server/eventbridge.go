package server

import (
	"context"
	"log/slog"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/events"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// Watch reconnect backoff: doubles from min to max on consecutive failures,
// resets to min once a watch is established.
const (
	bridgeBackoffMin = time.Second
	bridgeBackoffMax = 30 * time.Second
)

// bridgeState is the last-published status per VibedApp, keyed by UID so a
// delete + recreate under the same name still counts as a fresh app.
type bridgeState struct {
	phase string
	url   string
}

// eventBridge watches VibedApp resources in the apps namespace and publishes
// artifact lifecycle events onto the SSE event bus. The /v1 deploy path
// writes status through the controller, not the legacy orchestrator, so
// without this bridge live deploys never reach connected dashboards and the
// UI survives on its polling fallback.
type eventBridge struct {
	client    ctrlclient.WithWatch
	namespace string
	bus       *events.EventBus
	logger    *slog.Logger

	// last holds the last-published state per app; only publish when Phase or
	// URL genuinely changed (status writes touch other fields constantly).
	// Accessed only from the Run goroutine, so no lock is needed.
	last map[types.UID]bridgeState

	// watchStarted, when non-nil, is invoked after each successful Watch call.
	// Test hook: lets tests create objects only once the watch is receiving.
	watchStarted func()
}

// newEventBridge builds a bridge over a watch-capable client for VibedApp
// CRs — the deploy service's client, so one watch client serves both the
// readiness wait and the bridge.
func newEventBridge(c ctrlclient.WithWatch, namespace string, bus *events.EventBus, logger *slog.Logger) *eventBridge {
	return &eventBridge{
		client:    c,
		namespace: namespace,
		bus:       bus,
		logger:    logger,
		last:      make(map[types.UID]bridgeState),
	}
}

// Run watches VibedApps until ctx is cancelled, re-establishing the watch
// with capped exponential backoff whenever it fails or the server closes it.
func (b *eventBridge) Run(ctx context.Context) {
	backoff := bridgeBackoffMin
	for ctx.Err() == nil {
		established, err := b.watchOnce(ctx)
		if established {
			backoff = bridgeBackoffMin
		}
		if err != nil && ctx.Err() == nil {
			b.logger.Warn("VibedApp event bridge watch failed; retrying", "error", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > bridgeBackoffMax {
			backoff = bridgeBackoffMax
		}
	}
}

// watchOnce establishes a single watch session and consumes events until the
// watch closes, errors, or ctx is cancelled. established reports whether the
// watch was set up at all, so Run can reset its backoff.
func (b *eventBridge) watchOnce(ctx context.Context) (established bool, err error) {
	var list vibedv1.VibedAppList
	w, err := b.client.Watch(ctx, &list, ctrlclient.InNamespace(b.namespace))
	if err != nil {
		return false, err
	}
	defer w.Stop()
	if b.watchStarted != nil {
		b.watchStarted()
	}

	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case ev, ok := <-w.ResultChan():
			if !ok {
				// Server closed the watch (timeout, apiserver restart) —
				// return so Run re-establishes it.
				return true, nil
			}
			b.handle(ev)
		}
	}
}

// handle maps a single watch event onto the bus, suppressing status writes
// that don't change Phase or URL.
func (b *eventBridge) handle(ev watch.Event) {
	app, ok := ev.Object.(*vibedv1.VibedApp)
	if !ok {
		// watch.Error events carry *metav1.Status; Bookmark carries no app.
		return
	}

	switch ev.Type {
	case watch.Deleted:
		delete(b.last, app.UID)
		b.bus.Publish(events.Event{
			Type:       events.ArtifactDeleted,
			ArtifactID: app.Name, // the /v1 dashboard uses app_id (= CR name) as its artifact id
			OwnerID:    app.Spec.Owner,
			Timestamp:  time.Now(),
		})

	case watch.Added, watch.Modified:
		cur := bridgeState{phase: string(app.Status.Phase), url: app.Status.URL}
		if prev, seen := b.last[app.UID]; seen && prev == cur {
			return // status write with same Phase/URL — not a genuine transition
		}
		b.last[app.UID] = cur
		b.bus.Publish(events.Event{
			Type:       events.ArtifactStatusChanged,
			ArtifactID: app.Name,
			Name:       app.Name,
			OwnerID:    app.Spec.Owner,
			// The dashboard maps phases to display statuses client-side
			// (web phaseToStatus); there is no server-side mapping, so
			// Status carries the raw phase string.
			Status:    cur.phase,
			Phase:     cur.phase,
			URL:       cur.url,
			Template:  app.Spec.Runtime.Template,
			Timestamp: time.Now(),
		})
	}
}
