// Command vibed-router maintains Caddy's route table from VibedApp status.
//
// One vibed-router replica observes every VibedApp in the cluster and PUTs
// the corresponding route into Caddy via its admin API. The controller
// itself never touches Caddy — by splitting the responsibilities, the
// reconcile loop in vibed-controller stays fast (no network hop per
// reconcile) and the router can be horizontally scaled or restarted
// independently.
//
// Multiple replicas are safe: every operation is idempotent and addressed
// by @id, so concurrent reconciles converge on the same end state.
package main

import (
	"flag"
	"log/slog"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/vibed-project/vibeD/internal/router"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(vibedv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr string
		probeAddr   string
		caddyAdmin  string
		appPort     int
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8083", "Address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8084", "Address the probe endpoint binds to.")
	flag.StringVar(&caddyAdmin, "caddy-admin", "http://localhost:2019",
		"Base URL of Caddy's admin API. Default fits a Caddy sidecar; for a separate Deployment set this to the in-cluster Service URL.")
	flag.IntVar(&appPort, "app-port", 8080,
		"User-process port templates expose (matches templates/*/Dockerfile VIBED_AGENT_APP_PORT).")
	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		fatal(logger, "manager init", err)
	}

	if err := (&router.Reconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Caddy:   router.NewCaddyClient(caddyAdmin),
		AppPort: appPort,
	}).SetupWithManager(mgr); err != nil {
		fatal(logger, "reconciler setup", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		fatal(logger, "add healthz", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		fatal(logger, "add readyz", err)
	}

	ctx := ctrl.SetupSignalHandler()
	log.FromContext(ctx).Info("starting vibed-router", "metrics", metricsAddr, "probes", probeAddr, "caddy", caddyAdmin)
	if err := mgr.Start(ctx); err != nil {
		fatal(logger, "manager run", err)
	}
}

func fatal(logger *slog.Logger, what string, err error) {
	logger.Error(what+" failed", "error", err)
	os.Exit(1)
}
