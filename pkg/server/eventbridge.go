package server

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// delete + recreate under the same name still counts as a fresh app. name and
// owner are retained so the bridge can synthesize an ArtifactDeleted for an
// app that vanished while it was disconnected (its Deleted event was never
// observed, so the CR — and its Name/Owner — is already gone by reconcile).
type bridgeState struct {
	name  string
	owner string
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

// watchOnce runs one list-then-watch session and consumes events until the
// watch closes, errors, or ctx is cancelled. established reports whether the
// watch was set up at all, so Run can reset its backoff.
//
// It lists first, reconciles b.last against that snapshot (see sync), then
// anchors the watch at the list's ResourceVersion. Anchoring closes the gap
// between list and watch — the apiserver replays everything since that RV, not
// existing objects as synthetic ADDED — which is what lets sync, rather than a
// replayed ADDED per app, drive the reconcile after a reconnect.
func (b *eventBridge) watchOnce(ctx context.Context) (established bool, err error) {
	var list vibedv1.VibedAppList
	if err := b.client.List(ctx, &list, ctrlclient.InNamespace(b.namespace)); err != nil {
		return false, err
	}
	b.sync(list.Items)

	w, err := b.client.Watch(ctx, &vibedv1.VibedAppList{},
		ctrlclient.InNamespace(b.namespace),
		&ctrlclient.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: list.ResourceVersion}},
	)
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

// sync reconciles the last-published state against an authoritative list taken
// at the start of a watch session. Any app still in b.last but absent from the
// list was deleted while the bridge was disconnected — its Deleted event was
// never observed — so it is reaped with a synthetic ArtifactDeleted and dropped
// from b.last (otherwise the entry, and the dashboard's stale card, would
// linger for the process lifetime and the map would grow unbounded). Surviving
// apps run through the same transition logic as a Modified event, so a status
// that changed during the gap is republished exactly once.
func (b *eventBridge) sync(items []vibedv1.VibedApp) {
	present := make(map[types.UID]struct{}, len(items))
	for i := range items {
		present[items[i].UID] = struct{}{}
	}
	for uid, prev := range b.last {
		if _, ok := present[uid]; !ok {
			delete(b.last, uid)
			b.publishDelete(prev.name, prev.owner)
		}
	}
	for i := range items {
		b.publishStatus(&items[i])
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
		b.publishDelete(app.Name, app.Spec.Owner)
	case watch.Added, watch.Modified:
		b.publishStatus(app)
	}
}

// publishStatus records app's current Phase/URL and publishes an
// ArtifactStatusChanged only when they genuinely changed from the
// last-published state (status writes touch other fields constantly). Shared by
// live Added/Modified events and the reconnect reconcile so both dedup
// identically.
func (b *eventBridge) publishStatus(app *vibedv1.VibedApp) {
	cur := bridgeState{
		name:  app.Name,
		owner: app.Spec.Owner,
		phase: string(app.Status.Phase),
		url:   app.Status.URL,
	}
	if prev, seen := b.last[app.UID]; seen && prev == cur {
		return // status write with same Phase/URL — not a genuine transition
	}
	b.last[app.UID] = cur
	b.bus.Publish(events.Event{
		Type:       events.ArtifactStatusChanged,
		ArtifactID: app.Name, // the /v1 dashboard uses app_id (= CR name) as its artifact id
		Name:       app.Name,
		OwnerID:    app.Spec.Owner,
		// Status carries the display enum every other producer (and the
		// OpenAPI contract) speaks; Phase carries the raw CR phase for
		// clients that map it themselves.
		Status:    string(vibedv1.StatusFromPhase(app.Status.Phase)),
		Phase:     cur.phase,
		URL:       cur.url,
		Template:  app.Spec.Runtime.Template,
		Timestamp: time.Now(),
	})
}

// publishDelete emits an ArtifactDeleted for an app that is gone, from either a
// live Deleted event or the reconnect reconcile.
func (b *eventBridge) publishDelete(id, owner string) {
	b.bus.Publish(events.Event{
		Type:       events.ArtifactDeleted,
		ArtifactID: id, // the /v1 dashboard uses app_id (= CR name) as its artifact id
		OwnerID:    owner,
		Timestamp:  time.Now(),
	})
}
