// Package gc implements a resource garbage collector that backstops the
// owner-reference cascade for the live /v1 deploy path: it reaps orphaned
// SandboxClaim CRs whose owning VibedApp is gone but whose owner-ref cascade
// did not fire, so a stranded warm-pool pod is released back to the pool.
package gc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

const (
	// gcListPageSize bounds how many objects each GC List pulls per request, so
	// a large orphan backlog is streamed a page at a time instead of loaded
	// whole into memory (#76).
	gcListPageSize = 200
	// gcDeleteConcurrency bounds in-flight deletes per sweep so a backlog is
	// cleared with bounded parallelism instead of one blocking call at a time.
	gcDeleteConcurrency = 8
	// gcDeleteTimeout caps a single delete so one slow API call can't stall the
	// sweep and starve the next GC cycle.
	gcDeleteTimeout = 30 * time.Second
)

// deleteRunner runs per-resource deletes on a bounded worker pool, each under a
// per-delete timeout. Delete funcs are best-effort (they log their own errors),
// so the pool never aborts early on a single failure.
type deleteRunner struct {
	g   *errgroup.Group
	ctx context.Context
}

func newDeleteRunner(ctx context.Context) *deleteRunner {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(gcDeleteConcurrency)
	return &deleteRunner{g: g, ctx: gctx}
}

// run schedules one resource delete on the bounded pool with a per-delete
// timeout. It blocks only when all workers are busy (errgroup SetLimit).
func (d *deleteRunner) run(fn func(ctx context.Context)) {
	d.g.Go(func() error {
		ctx, cancel := context.WithTimeout(d.ctx, gcDeleteTimeout)
		defer cancel()
		fn(ctx)
		return nil // best-effort: never cancel siblings on one failure
	})
}

func (d *deleteRunner) wait() { _ = d.g.Wait() }

// labelApp is the label the controller's live /v1 path stamps on every
// SandboxClaim it creates (internal/controller: appLabelKey, see claim.go). Its
// value is the owning VibedApp's CR name, which is exactly the key the active
// set (listActiveApps) is built on — so a claim is an orphan iff its label
// value is not a live VibedApp name.
const labelApp = "vibed.dev/app"

// sandboxClaimGVR identifies the agent-sandbox SandboxClaim resource the live
// /v1 path creates (one per VibedApp, owner-referenced to it). Group/version
// mirror internal/controller.SandboxClaimGVK.
var sandboxClaimGVR = schema.GroupVersionResource{
	Group:    "extensions.agents.x-k8s.io",
	Version:  "v1beta1",
	Resource: "sandboxclaims",
}

// GarbageCollector periodically backstops the owner-reference cascade for the
// live /v1 deploy path.
//
// Live resources are owner-referenced to their VibedApp and cascade-deleted by
// Kubernetes when the app goes away, so in the normal case there is nothing to
// collect. The GC exists only for the failure case: if a VibedApp's owner-ref
// cascade did not fire, its SandboxClaim would be stranded — pinning a warm-pool
// pod forever. The GC reaps such a claim (releasing the pod back to the pool)
// once its owning VibedApp is gone from the authoritative live set and it is
// older than MaxAge.
type GarbageCollector struct {
	dynamicClient dynamic.Interface // reads/deletes SandboxClaim CRs; nil if agent-sandbox isn't installed
	ctrlClient    ctrlclient.Client // reads VibedApp CRs to build the live set
	appsNamespace string            // where /v1 creates VibedApp CRs and their SandboxClaims
	interval      time.Duration
	maxAge        time.Duration
	dryRun        bool
	metrics       *metrics.Metrics
	logger        *slog.Logger
}

// NewGarbageCollector creates a new GarbageCollector from the given config.
// dynamicClient is optional — pass nil to skip the SandboxClaim sweep (cluster
// doesn't have agent-sandbox installed). ctrlClient reads VibedApp CRs to build
// the authoritative live set; appsNamespace is where those CRs (and their
// SandboxClaims) live.
func NewGarbageCollector(
	dynamicClient dynamic.Interface,
	ctrlClient ctrlclient.Client,
	appsNamespace string,
	cfg config.GCConfig,
	m *metrics.Metrics,
	logger *slog.Logger,
) (*GarbageCollector, error) {
	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		return nil, fmt.Errorf("parsing gc interval %q: %w", cfg.Interval, err)
	}
	maxAge, err := time.ParseDuration(cfg.MaxAge)
	if err != nil {
		return nil, fmt.Errorf("parsing gc maxAge %q: %w", cfg.MaxAge, err)
	}

	return &GarbageCollector{
		dynamicClient: dynamicClient,
		ctrlClient:    ctrlClient,
		appsNamespace: appsNamespace,
		interval:      interval,
		maxAge:        maxAge,
		dryRun:        cfg.DryRun,
		metrics:       m,
		logger:        logger.With("component", "gc"),
	}, nil
}

// Run starts the GC loop, running a collection cycle at each interval.
// It blocks until ctx is cancelled.
func (gc *GarbageCollector) Run(ctx context.Context) {
	gc.logger.Info("garbage collector started",
		"interval", gc.interval,
		"maxAge", gc.maxAge,
		"dryRun", gc.dryRun,
	)
	ticker := time.NewTicker(gc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			gc.logger.Info("garbage collector stopped")
			return
		case <-ticker.C:
			gc.collect(ctx)
		}
	}
}

// collect runs a single GC cycle, reaping orphaned SandboxClaims.
//
// It first builds the authoritative live set from VibedApp CRs. If that list
// fails, the cycle is skipped entirely (fail-safe: no deletions) — deleting on
// a partial/empty view could reap a live app's claim. The claim-orphan sweep
// then reaps only claims whose owning VibedApp is absent from that set.
func (gc *GarbageCollector) collect(ctx context.Context) {
	gc.logger.Info("starting GC cycle")

	active, err := gc.listActiveApps(ctx)
	if err != nil {
		gc.logger.Error("failed to list VibedApp CRs for GC, skipping cycle (fail-safe: no deletions)", "error", err)
		return
	}

	claimsReaped := gc.cleanOrphanedClaims(ctx, active)
	gc.logger.Info("GC cycle complete",
		"activeApps", len(active),
		"claimsReaped", claimsReaped,
		"dryRun", gc.dryRun,
	)
}

// listActiveApps builds the authoritative live set — VibedApp CR names in the
// apps namespace — paged so a large fleet is streamed rather than loaded whole.
// Keyed by CR Name, which is exactly the vibed.dev/app label value the /v1 path
// stamps on each SandboxClaim. An error is returned so the caller can fail safe.
func (gc *GarbageCollector) listActiveApps(ctx context.Context) (map[string]bool, error) {
	active := make(map[string]bool)
	var cont string
	for {
		var list vibedv1.VibedAppList
		opts := []ctrlclient.ListOption{
			ctrlclient.InNamespace(gc.appsNamespace),
			ctrlclient.Limit(gcListPageSize),
		}
		if cont != "" {
			opts = append(opts, ctrlclient.Continue(cont))
		}
		if err := gc.ctrlClient.List(ctx, &list, opts...); err != nil {
			return nil, err
		}
		for i := range list.Items {
			active[list.Items[i].Name] = true
		}
		cont = list.Continue
		if cont == "" {
			break
		}
	}
	return active, nil
}

// cleanOrphanedClaims reaps live-path SandboxClaim CRs whose owning VibedApp is
// gone. Each claim carries the vibed.dev/app label (value = the owning VibedApp
// CR name) that the /v1 path stamps at create time. A claim is an orphan iff
// that value is not in the active set — i.e. the VibedApp was deleted but the
// owner-ref cascade failed to reap its claim, stranding a warm-pool pod.
// Deleting the claim releases the pod back to the pool.
//
// Safety: never touches a claim whose VibedApp is live (active[app]); a claim
// younger than MaxAge is spared (a delete racing an in-flight create would be
// wrong); DryRun logs without deleting; deletes run with bounded concurrency;
// and the sweep is skipped silently when the SandboxClaim CRD isn't installed.
// The caller has already failed safe if the active-app list errored.
//
// Returns the number of claims reaped (or, in dry-run, that would be).
func (gc *GarbageCollector) cleanOrphanedClaims(ctx context.Context, active map[string]bool) int {
	if gc.dynamicClient == nil {
		return 0
	}

	reaped := 0
	runner := newDeleteRunner(ctx)
	opts := metav1.ListOptions{LabelSelector: labelApp, Limit: gcListPageSize}
	for {
		list, err := gc.dynamicClient.Resource(sandboxClaimGVR).Namespace(gc.appsNamespace).List(ctx, opts)
		if err != nil {
			// SandboxClaim CRD not installed in this cluster: silent skip.
			if k8serrors.IsNotFound(err) {
				return reaped
			}
			gc.logger.Warn("failed to list sandboxclaims for GC", "error", err)
			break
		}

		for _, claim := range list.Items {
			app := claim.GetLabels()[labelApp]
			// A claim with no app label, or whose owning VibedApp is still live,
			// is not an orphan — never reap a live app's claim.
			if app == "" || active[app] {
				continue
			}
			// MaxAge guard: never reap a freshly-created claim in the cycle it
			// appears — a delete racing an in-flight create would be wrong.
			if age := time.Since(claim.GetCreationTimestamp().Time); age < gc.maxAge {
				continue
			}
			if gc.dryRun {
				gc.logger.Info("dry-run: would delete orphaned sandboxclaim", "claim", claim.GetName(), "app", app)
				reaped++
				continue
			}
			reaped++
			name := claim.GetName()
			runner.run(func(ctx context.Context) {
				if err := gc.dynamicClient.Resource(sandboxClaimGVR).Namespace(gc.appsNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
					if k8serrors.IsNotFound(err) {
						return
					}
					gc.logger.Warn("failed to delete orphaned sandboxclaim", "claim", name, "error", err)
					return
				}
				gc.metrics.GCResourcesCleaned.WithLabelValues("sandboxclaim").Inc()
				gc.logger.Info("deleted orphaned sandboxclaim", "claim", name, "app", app)
			})
		}

		if list.GetContinue() == "" {
			break
		}
		opts.Continue = list.GetContinue()
	}
	runner.wait()
	return reaped
}
