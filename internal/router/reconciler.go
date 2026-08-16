package router

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/vibed-project/vibeD/internal/controller"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// RoutePrefix is the @id namespace vibed-router owns inside Caddy. The
// reconciler refuses to delete any route whose @id doesn't start with this
// — keeps the router from clobbering routes someone added by hand.
//
// No '/' here: Caddy's admin /id/<id> endpoint treats '/' as a path
// separator, so a slashed @id (e.g. "vibed-app/<label>") makes PATCH and
// DELETE /id/... resolve the wrong config node — the route could never be
// updated or removed.
const RoutePrefix = "vibed-app-"

// Reconciler reconciles VibedApp resources into Caddy routes. It does not
// patch VibedApp status — read-only on the K8s side — so multiple router
// replicas can run concurrently without conflicting writes.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Caddy *CaddyClient

	// AppPort is the user-process port exposed by every template (8080 by
	// the templates/*/Dockerfile convention).
	AppPort int
}

// SetupWithManager wires the reconciler. We only watch VibedApp; the
// Caddy side is purely outbound (PATCH/DELETE).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AppPort == 0 {
		r.AppPort = 8080
	}
	return ctrl.NewControllerManagedBy(mgr).
		// The controller writes VibedApp status constantly (condition
		// LastTransitionTime bumps, transitional-phase churn); without a
		// predicate every one of those writes becomes a Reconcile → a Caddy
		// admin PATCH even though nothing routing-relevant changed. Filter
		// Update events down to the fields route computation actually reads.
		For(&vibedv1.VibedApp{}, builder.WithPredicates(routingPredicate())).
		Named("vibed-router").
		Complete(r)
}

// routingPredicate passes every Create/Delete/Generic event untouched
// (predicate.Funcs returns true for unset funcs) and admits Update events
// only when a routing-relevant field changed — see routingFieldsChanged.
func routingPredicate() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldApp, okOld := e.ObjectOld.(*vibedv1.VibedApp)
			newApp, okNew := e.ObjectNew.(*vibedv1.VibedApp)
			if !okOld || !okNew {
				// Shouldn't happen on a For(&VibedApp{}) watch; fail open —
				// a spurious reconcile beats a silently dropped event.
				return true
			}
			return routingFieldsChanged(oldApp, newApp)
		},
	}
}

// routingFieldsChanged reports whether an update touched any VibedApp field
// the router's route computation reads: Status.Phase (Ready gate), Status.URL
// (host matcher), and Status.RouteTarget / Status.PodIP (upstream, in that
// order — see upstream()). The route @id derives from namespace/name only,
// which are immutable on update.
//
// Two fields Reconcile does not read directly are still included out of
// caution — a false-positive reconcile is a cheap no-op PATCH, a missed one
// leaves a stale or missing route:
//   - DeletionTimestamp turning non-nil marks the start of teardown (final
//     cleanup still happens on the Delete event / IsNotFound path);
//   - Spec.Suspended is the desired-state precursor of a Phase flip.
func routingFieldsChanged(oldApp, newApp *vibedv1.VibedApp) bool {
	switch {
	case oldApp.Status.Phase != newApp.Status.Phase:
		return true
	case oldApp.Status.URL != newApp.Status.URL:
		return true
	case oldApp.Status.RouteTarget != newApp.Status.RouteTarget:
		return true
	case oldApp.Status.PodIP != newApp.Status.PodIP:
		return true
	case oldApp.DeletionTimestamp.IsZero() != newApp.DeletionTimestamp.IsZero():
		return true
	case oldApp.Spec.Suspended != newApp.Spec.Suspended:
		return true
	}
	return false
}

// Reconcile decides whether the app should have a route in Caddy, then
// pushes that state through the admin API.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("vibedapp", req.NamespacedName)

	var app vibedv1.VibedApp
	err := r.Get(ctx, req.NamespacedName, &app)
	switch {
	case apierrors.IsNotFound(err):
		// VibedApp deleted → drop the route. We synthesize the id from
		// the request key (which is namespace+name) so we don't need the
		// resource to compute the label. The owner-ref on SandboxClaim
		// handles pod cleanup; the router only does Caddy.
		id := RoutePrefix + labelFromKey(req.Namespace, req.Name)
		if dErr := r.Caddy.DeleteRoute(ctx, id); dErr != nil {
			return reconcile.Result{}, fmt.Errorf("delete route %s: %w", id, dErr)
		}
		logger.Info("dropped route for deleted app", "id", id)
		return reconcile.Result{}, nil
	case err != nil:
		return reconcile.Result{}, fmt.Errorf("get VibedApp: %w", err)
	}

	id := RoutePrefix + controller.AppLabel(&app)

	// Only Ready apps with a populated URL + routable upstream get a route.
	// Anything else means the controller hasn't finished spinning up; ensure
	// no stale route persists.
	upstream := r.upstream(&app)
	if app.Status.Phase != vibedv1.PhaseReady || app.Status.URL == "" || upstream == "" {
		if dErr := r.Caddy.DeleteRoute(ctx, id); dErr != nil {
			return reconcile.Result{}, fmt.Errorf("delete stale route %s: %w", id, dErr)
		}
		return reconcile.Result{}, nil
	}

	host := strings.TrimPrefix(app.Status.URL, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.SplitN(host, "/", 2)[0] // drop any path
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i] // drop any port — Caddy matches Host with the port stripped
	}

	route := Route{
		ID:       id,
		Host:     host,
		Upstream: upstream,
	}
	if err := r.Caddy.EnsureRoute(ctx, route); err != nil {
		return reconcile.Result{}, fmt.Errorf("ensure route %s: %w", id, err)
	}
	logger.Info("ensured route", "id", id, "host", host, "upstream", route.Upstream)
	return reconcile.Result{}, nil
}

// upstream resolves the routable target Caddy proxies to. RouteTarget is the
// stable, controller-published address (a per-app Service's cluster-DNS name,
// or the workerd host:port) and is preferred. PodIP is a legacy fallback for
// apps that predate RouteTarget: a bare pod IP gets AppPort appended; a value
// that already carries a port is used verbatim. Returns "" when neither is set.
func (r *Reconciler) upstream(app *vibedv1.VibedApp) string {
	if t := app.Status.RouteTarget; t != "" {
		return t
	}
	ip := app.Status.PodIP
	if ip == "" {
		return ""
	}
	if strings.Contains(ip, ":") {
		return ip
	}
	return fmt.Sprintf("%s:%d", ip, r.AppPort)
}

// labelFromKey reconstructs controller.AppLabel from just the namespaced
// name — used on the delete path where the VibedApp is already gone.
func labelFromKey(namespace, name string) string {
	stub := &vibedv1.VibedApp{}
	stub.Namespace = namespace
	stub.Name = name
	return controller.AppLabel(stub)
}
