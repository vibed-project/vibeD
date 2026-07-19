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

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
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
	// labelArtifactID is used to cross-reference resources against the store.
	labelArtifactID = "vibed.dev/artifact-id"
	// labelComponent identifies the resource type (e.g. "build").
	labelComponent = "vibed.dev/component"
	// labelStoreComponent marks artifact-store ConfigMaps that should not be GC'd.
	labelStoreComponent = "app.kubernetes.io/component"
)

// GarbageCollector periodically scans for orphaned K8s resources and removes them.
type GarbageCollector struct {
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface // optional; nil if not provided
	store         store.ArtifactStore
	namespace     string
	interval      time.Duration
	maxAge        time.Duration
	dryRun        bool
	metrics       *metrics.Metrics
	logger        *slog.Logger
}

// NewGarbageCollector creates a new GarbageCollector from the given config.
// dynamicClient is optional — pass nil to skip the Sandbox sweep (cluster
// doesn't have agent-sandbox installed).
func NewGarbageCollector(
	clientset kubernetes.Interface,
	dynamicClient dynamic.Interface,
	st store.ArtifactStore,
	namespace string,
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
		clientset:     clientset,
		dynamicClient: dynamicClient,
		store:         st,
		namespace:     namespace,
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

// collect runs a single GC cycle, cleaning up orphaned resources.
func (gc *GarbageCollector) collect(ctx context.Context) {
	gc.logger.Info("starting GC cycle")

	res, err := gc.store.List(ctx, store.ListOptions{AdminView: true, Limit: 0})
	if err != nil {
		gc.logger.Error("failed to list artifacts for GC, skipping cycle", "error", err)
		return
	}

	activeArtifacts := make(map[string]bool, len(res.Artifacts))
	for _, a := range res.Artifacts {
		activeArtifacts[a.ID] = true
	}

	gc.cleanOrphanedJobs(ctx, activeArtifacts)
	gc.cleanOrphanedConfigMaps(ctx, activeArtifacts)
	gc.cleanOrphanedDeployments(ctx, activeArtifacts)
	gc.cleanOrphanedSandboxes(ctx, activeArtifacts)
	gc.logger.Info("GC cycle complete")
}

// cleanOrphanedSandboxes deletes Sandbox CRs whose artifact no longer exists
// in the store. Skipped silently when the agent-sandbox CRD isn't installed.
func (gc *GarbageCollector) cleanOrphanedSandboxes(ctx context.Context, activeArtifacts map[string]bool) {
	if gc.dynamicClient == nil {
		return
	}
	sandboxGVR := schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1beta1",
		Resource: "sandboxes",
	}

	runner := newDeleteRunner(ctx)
	opts := metav1.ListOptions{LabelSelector: labelManagedBy, Limit: gcListPageSize}
	for {
		list, err := gc.dynamicClient.Resource(sandboxGVR).Namespace(gc.namespace).List(ctx, opts)
		if err != nil {
			// agent-sandbox CRD not installed in this cluster: silent skip.
			if k8serrors.IsNotFound(err) {
				return
			}
			gc.logger.Warn("failed to list sandboxes for GC", "error", err)
			break
		}

		for _, sb := range list.Items {
			artifactID := sb.GetLabels()[labelArtifactID]
			if artifactID == "" || activeArtifacts[artifactID] {
				continue
			}
			if gc.dryRun {
				gc.logger.Info("dry-run: would delete orphaned sandbox", "sandbox", sb.GetName(), "artifactID", artifactID)
				continue
			}
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
}

// cleanOrphanedJobs deletes completed/failed build Jobs whose artifact
// no longer exists in the store, or that are older than maxAge.
func (gc *GarbageCollector) cleanOrphanedJobs(ctx context.Context, activeArtifacts map[string]bool) {
	selector := labelManagedBy + "," + labelComponent + "=build"
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
				continue
			}

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
}

// cleanOrphanedConfigMaps deletes ConfigMaps whose artifact no longer exists.
func (gc *GarbageCollector) cleanOrphanedConfigMaps(ctx context.Context, activeArtifacts map[string]bool) {
	selector := labelManagedBy + "," + labelArtifactID
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
			if artifactID == "" || activeArtifacts[artifactID] {
				continue
			}

			if gc.dryRun {
				gc.logger.Info("dry-run: would delete orphaned configmap", "configmap", cm.Name, "artifactID", artifactID)
				continue
			}

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
}

// cleanOrphanedDeployments deletes Deployments (and their matching Services)
// whose artifact no longer exists in the store.
func (gc *GarbageCollector) cleanOrphanedDeployments(ctx context.Context, activeArtifacts map[string]bool) {
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
			if artifactID == "" || activeArtifacts[artifactID] {
				continue
			}

			if gc.dryRun {
				gc.logger.Info("dry-run: would delete orphaned deployment", "deployment", deploy.Name, "artifactID", artifactID)
				continue
			}

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

// isNotFound returns true if the error is an api.ErrNotFound.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*api.ErrNotFound)
	return ok
}
