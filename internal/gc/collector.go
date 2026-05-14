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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	knversioned "knative.dev/serving/pkg/client/clientset/versioned"

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

// PreviewReaper deletes a stale fast-path preview artifact, including
// recycling its pooled runner. The orchestrator satisfies this.
type PreviewReaper interface {
	ReapArtifact(ctx context.Context, artifactID string) error
}

// GarbageCollector periodically scans for orphaned K8s resources and removes them.
type GarbageCollector struct {
	clientset     kubernetes.Interface
	knClient      knversioned.Interface // optional; nil if Knative not installed
	dynamicClient dynamic.Interface     // optional; nil if not provided
	store         store.ArtifactStore
	namespace     string
	interval      time.Duration
	maxAge        time.Duration
	dryRun        bool
	metrics       *metrics.Metrics
	logger        *slog.Logger

	// previewReaper and previewMaxAge drive the stale-preview pass. When the
	// reaper is nil or the max-age is non-positive (fast path disabled) the
	// pass is skipped.
	previewReaper PreviewReaper
	previewMaxAge time.Duration
}

// NewGarbageCollector creates a new GarbageCollector from the given config.
// knClient and dynamicClient are optional — pass nil to skip the corresponding
// GC passes (e.g. nil knClient when Knative is not installed). previewReaper
// is optional — pass nil (or previewMaxAge <= 0) to skip the stale-preview
// pass when the fast path is disabled.
func NewGarbageCollector(
	clientset kubernetes.Interface,
	knClient knversioned.Interface,
	dynamicClient dynamic.Interface,
	st store.ArtifactStore,
	namespace string,
	cfg config.GCConfig,
	previewReaper PreviewReaper,
	previewMaxAge time.Duration,
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
		knClient:      knClient,
		dynamicClient: dynamicClient,
		store:         st,
		namespace:     namespace,
		interval:      interval,
		maxAge:        maxAge,
		dryRun:        cfg.DryRun,
		metrics:       m,
		logger:        logger.With("component", "gc"),
		previewReaper: previewReaper,
		previewMaxAge: previewMaxAge,
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
	gc.cleanOrphanedKnativeServices(ctx, activeArtifacts)
	gc.cleanOrphanedSandboxes(ctx, activeArtifacts)
	gc.cleanStalePreviews(ctx, res.Artifacts)
	gc.logger.Info("GC cycle complete")
}

// cleanStalePreviews reaps fast-path preview artifacts older than previewMaxAge.
// Reaping deletes the artifact and recycles its pooled runner (via the
// PreviewReaper). Skipped when the fast path is disabled.
func (gc *GarbageCollector) cleanStalePreviews(ctx context.Context, artifacts []api.ArtifactSummary) {
	if gc.previewReaper == nil || gc.previewMaxAge <= 0 {
		return
	}
	for _, a := range artifacts {
		if a.Mode != api.ModePreview {
			continue
		}
		age := time.Since(a.UpdatedAt)
		if age < gc.previewMaxAge {
			continue
		}
		if gc.dryRun {
			gc.logger.Info("dry-run: would reap stale preview", "artifact", a.Name, "id", a.ID, "age", age)
			continue
		}
		if err := gc.previewReaper.ReapArtifact(ctx, a.ID); err != nil {
			gc.logger.Warn("failed to reap stale preview", "artifact", a.Name, "id", a.ID, "error", err)
			continue
		}
		gc.metrics.GCResourcesCleaned.WithLabelValues("preview").Inc()
		gc.logger.Info("reaped stale preview", "artifact", a.Name, "id", a.ID, "age", age)
	}
}

// cleanOrphanedKnativeServices deletes Knative Services whose artifact no
// longer exists in the store. Skipped silently when no Knative client was
// supplied (e.g. cluster doesn't have Knative installed).
func (gc *GarbageCollector) cleanOrphanedKnativeServices(ctx context.Context, activeArtifacts map[string]bool) {
	if gc.knClient == nil {
		return
	}
	services, err := gc.knClient.ServingV1().Services(gc.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelManagedBy,
	})
	if err != nil {
		// CRD not installed: silent skip.
		if k8serrors.IsNotFound(err) {
			return
		}
		gc.logger.Warn("failed to list knative services for GC", "error", err)
		return
	}

	for _, svc := range services.Items {
		artifactID := svc.Labels[labelArtifactID]
		if artifactID == "" {
			continue
		}
		if activeArtifacts[artifactID] {
			continue
		}

		if gc.dryRun {
			gc.logger.Info("dry-run: would delete orphaned knative service", "service", svc.Name, "artifactID", artifactID)
			continue
		}

		if err := gc.knClient.ServingV1().Services(gc.namespace).Delete(ctx, svc.Name, metav1.DeleteOptions{}); err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			gc.logger.Warn("failed to delete orphaned knative service", "service", svc.Name, "error", err)
			continue
		}
		gc.metrics.GCResourcesCleaned.WithLabelValues("knative_service").Inc()
		gc.logger.Info("deleted orphaned knative service", "service", svc.Name, "artifactID", artifactID)
	}
}

// cleanOrphanedSandboxes deletes Sandbox CRs whose artifact no longer exists
// in the store. Skipped silently when the agent-sandbox CRD isn't installed.
func (gc *GarbageCollector) cleanOrphanedSandboxes(ctx context.Context, activeArtifacts map[string]bool) {
	if gc.dynamicClient == nil {
		return
	}
	sandboxGVR := schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}

	list, err := gc.dynamicClient.Resource(sandboxGVR).Namespace(gc.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelManagedBy,
	})
	if err != nil {
		// agent-sandbox CRD not installed in this cluster: silent skip.
		if k8serrors.IsNotFound(err) {
			return
		}
		gc.logger.Warn("failed to list sandboxes for GC", "error", err)
		return
	}

	for _, sb := range list.Items {
		labels := sb.GetLabels()
		artifactID := labels[labelArtifactID]
		if artifactID == "" {
			continue
		}
		if activeArtifacts[artifactID] {
			continue
		}

		if gc.dryRun {
			gc.logger.Info("dry-run: would delete orphaned sandbox", "sandbox", sb.GetName(), "artifactID", artifactID)
			continue
		}

		if err := gc.dynamicClient.Resource(sandboxGVR).Namespace(gc.namespace).Delete(ctx, sb.GetName(), metav1.DeleteOptions{}); err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			gc.logger.Warn("failed to delete orphaned sandbox", "sandbox", sb.GetName(), "error", err)
			continue
		}
		gc.metrics.GCResourcesCleaned.WithLabelValues("sandbox").Inc()
		gc.logger.Info("deleted orphaned sandbox", "sandbox", sb.GetName(), "artifactID", artifactID)
	}
}

// cleanOrphanedJobs deletes completed/failed build Jobs whose artifact
// no longer exists in the store, or that are older than maxAge.
func (gc *GarbageCollector) cleanOrphanedJobs(ctx context.Context, activeArtifacts map[string]bool) {
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
		exists := activeArtifacts[artifactID]
		orphaned := !exists
		stale := exists // artifact exists but job is old and finished

		if !orphaned && !stale {
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
func (gc *GarbageCollector) cleanOrphanedConfigMaps(ctx context.Context, activeArtifacts map[string]bool) {
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

		if activeArtifacts[artifactID] {
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
func (gc *GarbageCollector) cleanOrphanedDeployments(ctx context.Context, activeArtifacts map[string]bool) {
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

		if activeArtifacts[artifactID] {
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
