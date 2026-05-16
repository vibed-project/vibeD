// Command vibed-controller runs the VibedApp reconciler.
//
// The controller is the data-plane half of vibeD: vibed-api creates VibedApp
// CRs and this binary reconciles them through the lifecycle in refactor.md
// §5.3. It is meant to run as a Deployment with leader election enabled, so
// multiple replicas can be scheduled for HA without competing reconciles.
//
// In milestone B1 the reconciler ships with stub Claim/Probe/Router
// implementations — see internal/controller for the seams that later
// milestones fill in (Sandbox claiming in C, vibed-agent probing in B2,
// Caddy routing in D).
package main

import (
	"context"
	"flag"
	"fmt"
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

	"github.com/vibed-project/vibeD/internal/controller"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(vibedv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		domain               string
		poolNamespace        string
		agentToken           string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8081", "Address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8082", "Address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. Required for HA.")
	flag.StringVar(&domain, "vibed-domain", "vibed.example.com",
		"DNS suffix used to build app URLs in the stub Router.")
	flag.StringVar(&poolNamespace, "pool-namespace", "vibed-pools",
		"Namespace where SandboxWarmPool / SandboxTemplate live (matches templates/*/template.yaml).")
	flag.StringVar(&agentToken, "agent-token", "",
		"Bearer token vibed-agent expects on /healthz. Empty = no auth header sent.")
	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "vibed-controller.vibed.dev",
		LeaderElectionNamespace: os.Getenv("POD_NAMESPACE"),
	})
	if err != nil {
		fatal(logger, "manager init", err)
	}

	if err := (&controller.Reconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Claimer: &controller.SandboxClaimer{
			Client:        mgr.GetClient(),
			PoolNamespace: poolNamespace,
		},
		Probe:  controller.NewHTTPAgentProbe(agentToken),
		Router: controller.DeterministicRouter{Domain: domain},
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
	log.FromContext(ctx).Info("starting vibed-controller", "metrics", metricsAddr, "probes", probeAddr, "leader-elect", enableLeaderElection)
	if err := mgr.Start(ctx); err != nil {
		fatal(logger, "manager run", err)
	}
}

func fatal(logger *slog.Logger, what string, err error) {
	logger.Error(fmt.Sprintf("%s failed", what), "error", err)
	_ = context.Background()
	os.Exit(1)
}
