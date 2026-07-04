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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/vibed-project/vibeD/internal/controller"
	"github.com/vibed-project/vibeD/internal/meter"
	"github.com/vibed-project/vibeD/internal/templatevalidate"
	"github.com/vibed-project/vibeD/internal/workerd"
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
		urlScheme            string
		urlPort              string
		validateImages       bool
		strictValidation     bool
		poolNamespace        string
		agentToken           string
		workerdReplicas      int
		workerdControlPort   int
		workerdService       string
		workerdNamespace     string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8081", "Address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8082", "Address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. Required for HA.")
	flag.StringVar(&domain, "vibed-domain", "vibed.example.com",
		"DNS suffix used to build app URLs in the stub Router.")
	flag.StringVar(&urlScheme, "app-url-scheme", "https",
		"URL scheme for published app URLs. Set 'http' in dev (Caddy serves plain HTTP).")
	flag.StringVar(&urlPort, "app-url-port", "",
		"Optional port appended to app URLs (dev: the host port reaching Caddy, e.g. 18080). Empty in prod.")
	flag.BoolVar(&validateImages, "validate-images", true,
		"Validate warm-pool base images (BYO contract) and hard-gate deploys whose image is invalid. Disable for trusted/air-gapped installs.")
	flag.BoolVar(&strictValidation, "template-validation-strict", false,
		"Strict gate: deny claims for slots without a Valid recorded result AND when the warm-pool pod's current ImageID drifts from the validated digest (mutable-tag bypass guard). Default fail-open during the 2-minute warmup.")
	flag.StringVar(&poolNamespace, "pool-namespace", "vibed-pools",
		"Namespace where SandboxWarmPool / SandboxTemplate live (matches templates/*/template.yaml).")
	flag.StringVar(&agentToken, "agent-token", "",
		"Bearer token vibed-agent expects on /healthz. Empty = no auth header sent.")
	flag.IntVar(&workerdReplicas, "workerd-replicas", 0,
		"workerd StatefulSet size. 0 disables the fast lane (workerd apps fail to deploy).")
	flag.IntVar(&workerdControlPort, "workerd-control-port", 9200,
		"Loader control-API port on each workerd replica.")
	flag.StringVar(&workerdService, "workerd-service", "vibed-workerd",
		"Headless Service / StatefulSet name for workerd (used to build per-replica DNS).")
	flag.StringVar(&workerdNamespace, "workerd-namespace", "vibed-system",
		"Namespace the workerd StatefulSet runs in.")
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

	// Usage meter: Prometheus by default; an out-of-tree controller build can
	// register a different sink (e.g. a billing sink) to receive app lifecycle
	// events.
	meterSink, err := meter.Build(meter.Deps{})
	if err != nil {
		logger.Error("failed to build usage meter", "error", err)
		os.Exit(1)
	}

	reconciler := &controller.Reconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Claimer: &controller.SandboxClaimer{
			Client:        mgr.GetClient(),
			PoolNamespace: poolNamespace,
		},
		Probe:    controller.NewHTTPAgentProbe(agentToken),
		Injector: controller.NewHTTPInjector(agentToken),
		Services: &controller.K8sServiceManager{Client: mgr.GetClient(), AppPort: 8080},
		Router:   controller.DeterministicRouter{Domain: domain, Scheme: urlScheme, Port: urlPort},
		Meter:    meterSink,
	}
	if validateImages {
		reconciler.Gate = &controller.ConfigMapTemplateGate{
			Client:    mgr.GetClient(),
			Namespace: poolNamespace,
			Strict:    strictValidation,
		}
		if strictValidation {
			logger.Info("BYO template validation in STRICT mode: unvalidated or content-changed slots are denied")
		}
	}
	// Fast lane: wire the workerd loader client only when replicas are
	// configured. Otherwise the default DummyFastLaneDeployer rejects
	// workerd apps with a clear "not configured" error.
	if workerdReplicas > 0 {
		svc, ns := workerdService, workerdNamespace
		reconciler.FastLane = controller.WorkerdFastLane{
			Client: &workerd.Client{
				Replicas:    workerdReplicas,
				ControlPort: workerdControlPort,
				Token:       agentToken,
				ReplicaHost: func(idx int) string {
					// StatefulSet stable per-pod DNS:
					// <svc>-<idx>.<svc>.<ns>.svc.cluster.local
					return fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local", svc, idx, svc, ns)
				},
			},
		}
		logger.Info("workerd fast lane enabled", "replicas", workerdReplicas, "service", svc)
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		fatal(logger, "reconciler setup", err)
	}

	// Periodically validate the base image backing each warm-pool slot (BYO
	// base-image feature) and persist results for the claim-path gate. Runs
	// after the cache syncs; an initial pass happens immediately. Skipped when
	// --validate-images=false (trusted/air-gapped installs).
	if validateImages {
		templatevalidate.RegisterMetrics()
		validator := &templatevalidate.Validator{
			Client:    mgr.GetClient(),
			Namespace: poolNamespace,
			Probe:     templatevalidate.HTTPInfoProbe{Token: agentToken},
		}
		if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
			runValidation(ctx, validator, logger)
			t := time.NewTicker(2 * time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-t.C:
					runValidation(ctx, validator, logger)
				}
			}
		})); err != nil {
			fatal(logger, "add image validator", err)
		}
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

// runValidation validates every warm-pool image once and persists the results
// for the claim-path gate. Logs (never fatal) — a transient validation error
// must not take the controller down.
func runValidation(ctx context.Context, v *templatevalidate.Validator, logger *slog.Logger) {
	results, err := v.ValidateAll(ctx)
	if err != nil {
		logger.Warn("template image validation failed", "error", err)
		return
	}
	for _, r := range results {
		if !r.Valid {
			logger.Warn("template image invalid", "template", r.Template, "image", r.Image, "reason", r.Reason)
		}
	}
	templatevalidate.RecordResults(results)
	if err := v.Persist(ctx, results); err != nil {
		logger.Warn("persisting template validation results failed", "error", err)
	}
}

func fatal(logger *slog.Logger, what string, err error) {
	logger.Error(fmt.Sprintf("%s failed", what), "error", err)
	_ = context.Background()
	os.Exit(1)
}
