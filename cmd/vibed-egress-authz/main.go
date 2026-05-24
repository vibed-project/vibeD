// Command vibed-egress-authz answers per-connection egress authorization for
// the egress proxy (Squid). For each outbound connection the proxy asks
// GET /authz?src=<pod IP>&host=<dst host>; this service maps the source pod IP
// to the owning VibedApp's egress allow-list (status.podIP) and returns 200
// (allow) or 403 (deny). vibeD's system hosts (e.g. the source store) are
// always allowed. Default-deny.
//
// It reads from a cached, watched client, so per-request authorization is a
// local cache lookup, not an API call. Stateless and safe to run N replicas.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/vibed-project/vibeD/internal/egressauthz"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(vibedv1.AddToScheme(scheme))
}

func main() {
	// Subcommand: run as Squid's external_acl helper (bridges Squid's stdin
	// protocol to this service's /authz endpoint). Must precede flag.Parse.
	if len(os.Args) > 1 && os.Args[1] == "squid-helper" {
		os.Exit(runSquidHelper(os.Args[2:]))
	}

	var addr, metricsAddr, namespace, systemHostsCSV string
	flag.StringVar(&addr, "addr", ":8090", "authz HTTP listen address")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "metrics address (0 disables)")
	flag.StringVar(&namespace, "namespace", "vibed-apps", "workloads namespace to resolve VibedApps in")
	flag.StringVar(&systemHostsCSV, "system-hosts", "", "comma-separated hosts always allowed (e.g. the S3/MinIO source store)")
	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	systemHosts := splitCSV(systemHostsCSV)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: metricsAddr},
	})
	if err != nil {
		logger.Error("manager init failed", "error", err)
		os.Exit(1)
	}

	resolver := &egressauthz.K8sResolver{Client: mgr.GetClient(), Namespace: namespace}
	handler := egressauthz.NewHandler(resolver, systemHosts, logger)

	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			_ = srv.Close()
		}()
		logger.Info("egress authz listening", "addr", addr, "namespace", namespace, "systemHosts", systemHosts)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	})); err != nil {
		logger.Error("add http server failed", "error", err)
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error("manager run failed", "error", err)
		os.Exit(1)
	}
}

// runSquidHelper runs the external_acl helper loop (stdin/stdout), querying the
// authz service. Used as Squid's external_acl_type program inside the proxy pod.
func runSquidHelper(args []string) int {
	fs := flag.NewFlagSet("squid-helper", flag.ExitOnError)
	authzURL := fs.String("authz-url", "http://vibed-egress-authz:8090", "base URL of the egress authz service")
	_ = fs.Parse(args)
	if err := egressauthz.RunSquidHelper(context.Background(), os.Stdin, os.Stdout, *authzURL); err != nil {
		return 1
	}
	return 0
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
