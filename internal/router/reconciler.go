package router

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/vibed-project/vibeD/internal/controller"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// RoutePrefix is the @id namespace vibed-router owns inside Caddy. The
// reconciler refuses to delete any route whose @id doesn't start with this
// — keeps the router from clobbering routes someone added by hand.
const RoutePrefix = "vibed-app/"

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
		For(&vibedv1.VibedApp{}).
		Named("vibed-router").
		Complete(r)
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

	// Only Ready apps with a populated URL + PodIP get a route. Anything
	// else means the controller hasn't finished spinning up; ensure no
	// stale route persists.
	if app.Status.Phase != vibedv1.PhaseReady || app.Status.URL == "" || app.Status.PodIP == "" {
		if dErr := r.Caddy.DeleteRoute(ctx, id); dErr != nil {
			return reconcile.Result{}, fmt.Errorf("delete stale route %s: %w", id, dErr)
		}
		return reconcile.Result{}, nil
	}

	host := strings.TrimPrefix(app.Status.URL, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.SplitN(host, "/", 2)[0] // drop any path

	route := Route{
		ID:       id,
		Host:     host,
		Upstream: fmt.Sprintf("%s:%d", app.Status.PodIP, r.AppPort),
	}
	if err := r.Caddy.EnsureRoute(ctx, route); err != nil {
		return reconcile.Result{}, fmt.Errorf("ensure route %s: %w", id, err)
	}
	logger.Info("ensured route", "id", id, "host", host, "upstream", route.Upstream)
	return reconcile.Result{}, nil
}

// labelFromKey reconstructs controller.AppLabel from just the namespaced
// name — used on the delete path where the VibedApp is already gone.
func labelFromKey(namespace, name string) string {
	stub := &vibedv1.VibedApp{}
	stub.Namespace = namespace
	stub.Name = name
	return controller.AppLabel(stub)
}
