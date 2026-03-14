// Package gc implements a resource garbage collector that cleans up
// orphaned Kubernetes resources left behind by crashed deploys or
// partial delete failures.
package gc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
)

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
	clientset kubernetes.Interface
	store     store.ArtifactStore
	namespace string
	interval  time.Duration
	maxAge    time.Duration
	dryRun    bool
	metrics   *metrics.Metrics
	logger    *slog.Logger
}

// NewGarbageCollector creates a new GarbageCollector from the given config.
func NewGarbageCollector(
	clientset kubernetes.Interface,
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
		clientset: clientset,
		store:     st,
		namespace: namespace,
		interval:  interval,
		maxAge:    maxAge,
		dryRun:    cfg.DryRun,
		metrics:   m,
		logger:    logger.With("component", "gc"),
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
	gc.cleanOrphanedJobs(ctx)
	gc.cleanOrphanedConfigMaps(ctx)
	gc.cleanOrphanedDeployments(ctx)
	gc.logger.Info("GC cycle complete")
}

// cleanOrphanedJobs deletes completed/failed build Jobs whose artifact
// no longer exists in the store, or that are older than maxAge.
func (gc *GarbageCollector) cleanOrphanedJobs(ctx context.Context) {
	selector := labelManagedBy + "," + labelComponent + "=build"
	jobs, err := gc.clientset.BatchV1().Jobs(gc.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		gc.logger.Warn("failed to list jobs for GC", "error", err)
		return
	}

	for _, job := range jobs.Items {
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

		// Check if the artifact still exists.
		_, err := gc.store.Get(ctx, artifactID)
		orphaned := isNotFound(err)
		stale := err == nil // artifact exists but job is old and finished

		if !orphaned && !stale {
			// Unexpected error from store; skip.
			if err != nil {
				gc.logger.Warn("failed to check artifact for job GC", "job", job.Name, "error", err)
			}
			continue
		}

		if gc.dryRun {
			gc.logger.Info("dry-run: would delete orphaned job", "job", job.Name, "artifactID", artifactID, "age", age)
			continue
		}

		propagation := metav1.DeletePropagationBackground
		if err := gc.clientset.BatchV1().Jobs(gc.namespace).Delete(ctx, job.Name, metav1.DeleteOptions{
			PropagationPolicy: &propagation,
		}); err != nil {
			gc.logger.Warn("failed to delete orphaned job", "job", job.Name, "error", err)
			continue
		}
		gc.metrics.GCResourcesCleaned.WithLabelValues("job").Inc()
		gc.logger.Info("deleted orphaned job", "job", job.Name, "artifactID", artifactID)
	}
}

// cleanOrphanedConfigMaps deletes ConfigMaps whose artifact no longer exists.
func (gc *GarbageCollector) cleanOrphanedConfigMaps(ctx context.Context) {
	selector := labelManagedBy + "," + labelArtifactID
	cms, err := gc.clientset.CoreV1().ConfigMaps(gc.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		gc.logger.Warn("failed to list configmaps for GC", "error", err)
		return
	}

	for _, cm := range cms.Items {
		// Skip artifact-store ConfigMaps (metadata store, not deploy CMs).
		if cm.Labels[labelStoreComponent] == "artifact-store" {
			continue
		}

		artifactID := cm.Labels[labelArtifactID]
		if artifactID == "" {
			continue
		}

		_, err := gc.store.Get(ctx, artifactID)
		if !isNotFound(err) {
			if err != nil {
				gc.logger.Warn("failed to check artifact for configmap GC", "configmap", cm.Name, "error", err)
			}
			continue
		}

		if gc.dryRun {
			gc.logger.Info("dry-run: would delete orphaned configmap", "configmap", cm.Name, "artifactID", artifactID)
			continue
		}

		if err := gc.clientset.CoreV1().ConfigMaps(gc.namespace).Delete(ctx, cm.Name, metav1.DeleteOptions{}); err != nil {
			gc.logger.Warn("failed to delete orphaned configmap", "configmap", cm.Name, "error", err)
			continue
		}
		gc.metrics.GCResourcesCleaned.WithLabelValues("configmap").Inc()
		gc.logger.Info("deleted orphaned configmap", "configmap", cm.Name, "artifactID", artifactID)
	}
}

// cleanOrphanedDeployments deletes Deployments (and their matching Services)
// whose artifact no longer exists in the store.
func (gc *GarbageCollector) cleanOrphanedDeployments(ctx context.Context) {
	deployments, err := gc.clientset.AppsV1().Deployments(gc.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelManagedBy,
	})
	if err != nil {
		gc.logger.Warn("failed to list deployments for GC", "error", err)
		return
	}

	for _, deploy := range deployments.Items {
		artifactID := deploy.Labels[labelArtifactID]
		if artifactID == "" {
			continue
		}

		_, err := gc.store.Get(ctx, artifactID)
		if !isNotFound(err) {
			if err != nil {
				gc.logger.Warn("failed to check artifact for deployment GC", "deployment", deploy.Name, "error", err)
			}
			continue
		}

		if gc.dryRun {
			gc.logger.Info("dry-run: would delete orphaned deployment", "deployment", deploy.Name, "artifactID", artifactID)
			continue
		}

		// Delete the Deployment.
		if err := gc.clientset.AppsV1().Deployments(gc.namespace).Delete(ctx, deploy.Name, metav1.DeleteOptions{}); err != nil {
			gc.logger.Warn("failed to delete orphaned deployment", "deployment", deploy.Name, "error", err)
			continue
		}
		gc.metrics.GCResourcesCleaned.WithLabelValues("deployment").Inc()
		gc.logger.Info("deleted orphaned deployment", "deployment", deploy.Name, "artifactID", artifactID)

		// Also delete the matching Service (same name convention used by kubernetes deployer).
		if err := gc.clientset.CoreV1().Services(gc.namespace).Delete(ctx, deploy.Name, metav1.DeleteOptions{}); err != nil {
			gc.logger.Warn("failed to delete matching service", "service", deploy.Name, "error", err)
		} else {
			gc.metrics.GCResourcesCleaned.WithLabelValues("service").Inc()
			gc.logger.Info("deleted orphaned service", "service", deploy.Name, "artifactID", artifactID)
		}
	}
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
