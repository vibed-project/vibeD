// Package gc implements a resource garbage collector that cleans up
// orphaned Kubernetes resources left behind by crashed deploys or
// partial delete failures.
package gc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"
	batchv1 "k8s.io/api/batch/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
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

const (
	// labelManagedBy is used to identify vibeD-managed resources.
	labelManagedBy = "app.kubernetes.io/managed-by=vibed"
	// labelArtifactID cross-references a legacy resource against the live
	// VibedApp CR set: pre-v0.7 orchestrator/deployer resources carry it, and a
	// resource whose value is not a live VibedApp CR name is an orphan.
	labelArtifactID = "vibed.dev/artifact-id"
	// labelComponent identifies the resource type (e.g. "build").
	labelComponent = "vibed.dev/component"
	// labelStoreComponent marks artifact-store ConfigMaps that should not be GC'd.
	labelStoreComponent = "app.kubernetes.io/component"
)

// GarbageCollector periodically scans for orphaned K8s resources and removes them.
//
// The authoritative live set is the VibedApp CRs (read via ctrlClient): a
// resource is an orphan when its vibed.dev/artifact-id is not a live VibedApp
// CR name. Since the /v1 path never stamps that label — live apps own
// SandboxClaims/Services that Kubernetes cascade-deletes — every artifact-id
// sweep here targets pre-v0.7 orchestrator/deployer debris and is gated behind
// GCConfig.LegacySweeps.
type GarbageCollector struct {
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface // optional; nil if not provided
	ctrlClient    ctrlclient.Client // reads VibedApp CRs to build the live set
	namespace     string            // where legacy orchestrator/deployer resources live
	appsNamespace string            // where /v1 creates VibedApp CRs
	interval      time.Duration
	maxAge        time.Duration
	dryRun        bool
	legacySweeps  bool
	metrics       *metrics.Metrics
	logger        *slog.Logger
}

// NewGarbageCollector creates a new GarbageCollector from the given config.
// dynamicClient is optional — pass nil to skip the Sandbox sweep (cluster
// doesn't have agent-sandbox installed). ctrlClient reads VibedApp CRs to build
// the authoritative live set; appsNamespace is where those CRs live.
func NewGarbageCollector(
	clientset kubernetes.Interface,
	dynamicClient dynamic.Interface,
	ctrlClient ctrlclient.Client,
	namespace string,
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
	if appsNamespace == "" {
		appsNamespace = namespace
	}

	return &GarbageCollector{
		clientset:     clientset,
		dynamicClient: dynamicClient,
		ctrlClient:    ctrlClient,
		namespace:     namespace,
		appsNamespace: appsNamespace,
		interval:      interval,
		maxAge:        maxAge,
		dryRun:        cfg.DryRun,
		legacySweeps:  cfg.LegacySweeps,
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
		"legacySweeps", gc.legacySweeps,
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

// collect runs a single GC cycle, cleaning up orphaned resources.
//
// It first builds the authoritative live set from VibedApp CRs. If that list
// fails, the cycle is skipped entirely (fail-safe: no deletions) — deleting on
// a partial/empty view could reap live resources. The legacy sweeps then reap
// pre-v0.7 orchestrator/deployer debris and are gated behind LegacySweeps.
func (gc *GarbageCollector) collect(ctx context.Context) {
	gc.logger.Info("starting GC cycle")

	active, err := gc.listActiveApps(ctx)
	if err != nil {
		gc.logger.Error("failed to list VibedApp CRs for GC, skipping cycle (fail-safe: no deletions)", "error", err)
		return
	}

	if !gc.legacySweeps {
		gc.logger.Info("legacy sweeps disabled; skipping pre-v0.7 orphan cleanup", "activeApps", len(active))
		gc.logger.Info("GC cycle complete")
		return
	}

	// The four artifact-id sweeps target purely-legacy resources: with the
	// orchestrator gone nothing stamps vibed.dev/artifact-id, so any labelled
	// resource is legacy debris, reaped once past MaxAge. The sandbox sweep is
	// grouped here too — the /v1 path uses SandboxClaims (owner-referenced to
	// their VibedApp, cascade-deleted by Kubernetes), so the managed-by=vibed +
	// artifact-id Sandbox CRs it targets are exclusively the legacy deployer's.
	nJobs := gc.cleanOrphanedJobs(ctx, active)
	nCMs := gc.cleanOrphanedConfigMaps(ctx, active)
	nDeps := gc.cleanOrphanedDeployments(ctx, active)
	nSbx := gc.cleanOrphanedSandboxes(ctx, active)
	gc.logger.Info("legacy sweeps complete",
		"activeApps", len(active),
		"jobs", nJobs,
		"configmaps", nCMs,
		"deployments", nDeps,
		"sandboxes", nSbx,
		"dryRun", gc.dryRun,
	)
	gc.logger.Info("GC cycle complete")
}

// listActiveApps builds the authoritative live set — VibedApp CR names in the
// apps namespace — paged so a large fleet is streamed rather than loaded whole.
// Keyed by CR Name, which the /v1 path (and the event bridge) uses as the
// artifact id. An error is returned so the caller can fail safe.
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

// cleanOrphanedSandboxes deletes legacy Sandbox CRs (managed-by=vibed +
// artifact-id, created by the pre-v0.7 deployer) whose artifact-id is not a
// live VibedApp CR name and that are older than maxAge. Skipped silently when
// the agent-sandbox CRD isn't installed. Returns the number reaped (or, in
// dry-run, the number that would be). Live /v1 sandboxes are SandboxClaims
// owner-referenced to their VibedApp, so they aren't touched here.
func (gc *GarbageCollector) cleanOrphanedSandboxes(ctx context.Context, active map[string]bool) int {
	if gc.dynamicClient == nil {
		return 0
	}
	sandboxGVR := schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1beta1",
		Resource: "sandboxes",
	}

	reaped := 0
	runner := newDeleteRunner(ctx)
	opts := metav1.ListOptions{LabelSelector: labelManagedBy, Limit: gcListPageSize}
	for {
		list, err := gc.dynamicClient.Resource(sandboxGVR).Namespace(gc.namespace).List(ctx, opts)
		if err != nil {
			// agent-sandbox CRD not installed in this cluster: silent skip.
			if k8serrors.IsNotFound(err) {
				return reaped
			}
			gc.logger.Warn("failed to list sandboxes for GC", "error", err)
			break
		}

		for _, sb := range list.Items {
			artifactID := sb.GetLabels()[labelArtifactID]
			if artifactID == "" || active[artifactID] {
				continue
			}
			// MaxAge guard: never reap a freshly-created resource, even if
			// orphaned — a delete racing an in-flight create would be wrong.
			if age := time.Since(sb.GetCreationTimestamp().Time); age < gc.maxAge {
				continue
			}
			if gc.dryRun {
				gc.logger.Info("dry-run: would delete orphaned sandbox", "sandbox", sb.GetName(), "artifactID", artifactID)
				reaped++
				continue
			}
			reaped++
			name := sb.GetName()
			runner.run(func(ctx context.Context) {
				if err := gc.dynamicClient.Resource(sandboxGVR).Namespace(gc.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
					if k8serrors.IsNotFound(err) {
						return
					}
					gc.logger.Warn("failed to delete orphaned sandbox", "sandbox", name, "error", err)
					return
				}
				gc.metrics.GCResourcesCleaned.WithLabelValues("sandbox").Inc()
				gc.logger.Info("deleted orphaned sandbox", "sandbox", name, "artifactID", artifactID)
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

// cleanOrphanedJobs deletes completed/failed legacy build Jobs whose
// artifact-id is not a live VibedApp CR name, or that are older than maxAge.
// Returns the number reaped (or, in dry-run, the number that would be).
func (gc *GarbageCollector) cleanOrphanedJobs(ctx context.Context, active map[string]bool) int {
	selector := labelManagedBy + "," + labelComponent + "=build"
	reaped := 0
	runner := newDeleteRunner(ctx)
	opts := metav1.ListOptions{LabelSelector: selector, Limit: gcListPageSize}
	for {
		jobs, err := gc.clientset.BatchV1().Jobs(gc.namespace).List(ctx, opts)
		if err != nil {
			gc.logger.Warn("failed to list jobs for GC", "error", err)
			break
		}

		for i := range jobs.Items {
			job := jobs.Items[i]
			// Skip running jobs.
			if !isJobFinished(&job) {
				continue
			}

			artifactID := job.Labels[labelArtifactID]
			if artifactID == "" {
				continue
			}

			age := time.Since(job.CreationTimestamp.Time)
			if age < gc.maxAge {
				continue
			}
			// A finished job past maxAge is collected whether its artifact is gone
			// (orphaned) or still present (stale build job).

			if gc.dryRun {
				gc.logger.Info("dry-run: would delete orphaned job", "job", job.Name, "artifactID", artifactID, "age", age)
				reaped++
				continue
			}

			reaped++
			name := job.Name
			runner.run(func(ctx context.Context) {
				propagation := metav1.DeletePropagationBackground
				if err := gc.clientset.BatchV1().Jobs(gc.namespace).Delete(ctx, name, metav1.DeleteOptions{
					PropagationPolicy: &propagation,
				}); err != nil {
					gc.logger.Warn("failed to delete orphaned job", "job", name, "error", err)
					return
				}
				gc.metrics.GCResourcesCleaned.WithLabelValues("job").Inc()
				gc.logger.Info("deleted orphaned job", "job", name, "artifactID", artifactID)
			})
		}

		if jobs.Continue == "" {
			break
		}
		opts.Continue = jobs.Continue
	}
	runner.wait()
	return reaped
}

// cleanOrphanedConfigMaps deletes legacy ConfigMaps whose artifact-id is not a
// live VibedApp CR name. Returns the number reaped (or, in dry-run, would be).
func (gc *GarbageCollector) cleanOrphanedConfigMaps(ctx context.Context, active map[string]bool) int {
	selector := labelManagedBy + "," + labelArtifactID
	reaped := 0
	runner := newDeleteRunner(ctx)
	opts := metav1.ListOptions{LabelSelector: selector, Limit: gcListPageSize}
	for {
		cms, err := gc.clientset.CoreV1().ConfigMaps(gc.namespace).List(ctx, opts)
		if err != nil {
			gc.logger.Warn("failed to list configmaps for GC", "error", err)
			break
		}

		for _, cm := range cms.Items {
			// Skip artifact-store ConfigMaps (metadata store, not deploy CMs).
			if cm.Labels[labelStoreComponent] == "artifact-store" {
				continue
			}

			artifactID := cm.Labels[labelArtifactID]
			if artifactID == "" || active[artifactID] {
				continue
			}
			// MaxAge guard: spare a freshly-created ConfigMap even if orphaned.
			if age := time.Since(cm.CreationTimestamp.Time); age < gc.maxAge {
				continue
			}

			if gc.dryRun {
				gc.logger.Info("dry-run: would delete orphaned configmap", "configmap", cm.Name, "artifactID", artifactID)
				reaped++
				continue
			}

			reaped++
			name := cm.Name
			runner.run(func(ctx context.Context) {
				if err := gc.clientset.CoreV1().ConfigMaps(gc.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
					gc.logger.Warn("failed to delete orphaned configmap", "configmap", name, "error", err)
					return
				}
				gc.metrics.GCResourcesCleaned.WithLabelValues("configmap").Inc()
				gc.logger.Info("deleted orphaned configmap", "configmap", name, "artifactID", artifactID)
			})
		}

		if cms.Continue == "" {
			break
		}
		opts.Continue = cms.Continue
	}
	runner.wait()
	return reaped
}

// cleanOrphanedDeployments deletes legacy Deployments (and their matching
// Services) whose artifact-id is not a live VibedApp CR name. Returns the
// number of Deployments reaped (or, in dry-run, would be).
func (gc *GarbageCollector) cleanOrphanedDeployments(ctx context.Context, active map[string]bool) int {
	reaped := 0
	runner := newDeleteRunner(ctx)
	opts := metav1.ListOptions{LabelSelector: labelManagedBy, Limit: gcListPageSize}
	for {
		deployments, err := gc.clientset.AppsV1().Deployments(gc.namespace).List(ctx, opts)
		if err != nil {
			gc.logger.Warn("failed to list deployments for GC", "error", err)
			break
		}

		for _, deploy := range deployments.Items {
			artifactID := deploy.Labels[labelArtifactID]
			if artifactID == "" || active[artifactID] {
				continue
			}
			// MaxAge guard: spare a freshly-created Deployment even if orphaned.
			if age := time.Since(deploy.CreationTimestamp.Time); age < gc.maxAge {
				continue
			}

			if gc.dryRun {
				gc.logger.Info("dry-run: would delete orphaned deployment", "deployment", deploy.Name, "artifactID", artifactID)
				reaped++
				continue
			}

			reaped++
			name := deploy.Name
			runner.run(func(ctx context.Context) {
				// Delete the Deployment.
				if err := gc.clientset.AppsV1().Deployments(gc.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
					gc.logger.Warn("failed to delete orphaned deployment", "deployment", name, "error", err)
					return
				}
				gc.metrics.GCResourcesCleaned.WithLabelValues("deployment").Inc()
				gc.logger.Info("deleted orphaned deployment", "deployment", name, "artifactID", artifactID)

				// Also delete the matching Service (same name convention used by the kubernetes deployer).
				if err := gc.clientset.CoreV1().Services(gc.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
					gc.logger.Warn("failed to delete matching service", "service", name, "error", err)
				} else {
					gc.metrics.GCResourcesCleaned.WithLabelValues("service").Inc()
					gc.logger.Info("deleted orphaned service", "service", name, "artifactID", artifactID)
				}
			})
		}

		if deployments.Continue == "" {
			break
		}
		opts.Continue = deployments.Continue
	}
	runner.wait()
	return reaped
}

// isJobFinished returns true if the Job has completed or failed.
func isJobFinished(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == "True" {
			return true
		}
	}
	return false
}
