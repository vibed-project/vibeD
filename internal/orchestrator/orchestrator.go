package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/builder"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/environment"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/storage"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/daemon"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var dnsNameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// DeployRequest is the input for deploying a new artifact.
type DeployRequest struct {
	Name       string
	Files      map[string]string
	Language   string
	Target     string
	EnvVars    map[string]string
	SecretRefs map[string]string // env var name → "secret-name:key"
	Port       int
}

// UpdateRequest is the input for updating an existing artifact.
type UpdateRequest struct {
	ArtifactID string
	Files      map[string]string
	EnvVars    map[string]string
	SecretRefs map[string]string
}

// DeployResult is the output of a successful deployment.
type DeployResult struct {
	ArtifactID string
	Name       string
	URL        string
	Target     string
	Status     string
	ImageRef   string
}

// Orchestrator coordinates the full deploy/update/delete lifecycle.
type Orchestrator struct {
	cfg         *config.Config
	detector    *environment.Detector
	builder builder.Builder
	factory *deployer.Factory
	storage     storage.Storage
	store       store.ArtifactStore
	metrics     *metrics.Metrics
	clientset   kubernetes.Interface
	events         *events.EventBus
	shareLinkStore store.ShareLinkStore
	imageBase      string
	tracer         trace.Tracer
	logger         *slog.Logger
}

// NewOrchestrator creates a new Orchestrator with all subsystems wired.
func NewOrchestrator(
	cfg *config.Config,
	detector *environment.Detector,
	bldr builder.Builder,
	factory *deployer.Factory,
	stg storage.Storage,
	st store.ArtifactStore,
	m *metrics.Metrics,
	clientset kubernetes.Interface,
	bus *events.EventBus,
	shareLinkStore store.ShareLinkStore,
	logger *slog.Logger,
) *Orchestrator {
	imageBase := "vibed-artifacts"
	if cfg.Registry.Enabled && cfg.Registry.URL != "" {
		imageBase = cfg.Registry.URL
	}

	return &Orchestrator{
		cfg:      cfg,
		detector: detector,
		builder:  bldr,
		factory:  factory,
		storage:     stg,
		store:       st,
		metrics:     m,
		clientset:   clientset,
		events:         bus,
		shareLinkStore: shareLinkStore,
		imageBase:      imageBase,
		tracer:         otel.Tracer("vibed/orchestrator"),
		logger:      logger,
	}
}

// Deploy handles the full deployment flow: validate → store → build → deploy.
func (o *Orchestrator) Deploy(ctx context.Context, req DeployRequest) (*DeployResult, error) {
	ctx, span := o.tracer.Start(ctx, "orchestrator.Deploy",
		trace.WithAttributes(
			attribute.String("artifact.name", req.Name),
			attribute.String("artifact.language", req.Language),
			attribute.String("artifact.target", req.Target),
		))
	defer span.End()

	traceID := span.SpanContext().TraceID().String()
	o.logger.Info("deploy started", "artifact", req.Name, "trace_id", traceID)

	result, err := o.doDeploy(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		o.logger.Error("deploy failed", "artifact", req.Name, "trace_id", traceID, "error", err)
	} else {
		o.logger.Info("deploy completed", "artifact", req.Name, "trace_id", traceID, "url", result.URL)
	}
	return result, err
}

// AsyncDeploy validates input and creates the artifact record synchronously, then runs
// the slow build + push + deploy in a background goroutine. Returns immediately with
// status "building" and the artifact_id so the caller can poll get_artifact_status.
// This prevents MCP client timeouts on long-running builds.
func (o *Orchestrator) AsyncDeploy(ctx context.Context, req DeployRequest) (*DeployResult, error) {
	// Fast pre-flight checks so callers get immediate errors for bad input.
	if err := validateName(req.Name); err != nil {
		return nil, err
	}
	if len(req.Files) == 0 {
		return nil, &api.ErrInvalidInput{Field: "files", Message: "at least one file is required"}
	}

	// Detect language so doDeploy doesn't re-detect (avoids double-scanning large file maps).
	lang := req.Language
	if lang == "" || lang == "auto" {
		lang = builder.DetectLanguage(req.Files)
	}
	req.Language = lang

	// Capture user identity before the request context is cancelled.
	ownerID := vibedauth.UserIDFromContext(ctx)
	bgCtx := vibedauth.WithUserID(context.Background(), ownerID)

	bgCtx, span := o.tracer.Start(bgCtx, "orchestrator.Deploy",
		trace.WithAttributes(
			attribute.String("artifact.name", req.Name),
			attribute.String("artifact.language", req.Language),
			attribute.String("artifact.target", req.Target),
		))
	traceID := span.SpanContext().TraceID().String()
	o.logger.Info("async deploy started", "artifact", req.Name, "trace_id", traceID)

	// Pre-create the artifact record synchronously so we can return the ID immediately.
	artifactID := generateID()
	now := time.Now()
	artifact := &api.Artifact{
		ID:         artifactID,
		Name:       req.Name,
		OwnerID:    ownerID,
		Status:     api.StatusBuilding,
		Language:   lang,
		EnvVars:    req.EnvVars,
		SecretRefs: req.SecretRefs,
		Port:       req.Port,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := o.store.Create(ctx, artifact); err != nil {
		span.End()
		return nil, err
	}

	// Run the full deploy (including the already-created artifact) in the background.
	// doDeploy will detect the existing record via GetByName and overwrite it.
	go func() {
		defer span.End()
		result, err := o.doDeploy(bgCtx, req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			o.logger.Error("async deploy failed", "artifact", req.Name, "trace_id", traceID, "error", err)
		} else {
			span.SetStatus(codes.Ok, "")
			o.logger.Info("async deploy completed", "artifact", req.Name, "trace_id", traceID, "url", result.URL)
		}
	}()

	return &DeployResult{
		ArtifactID: artifactID,
		Name:       req.Name,
		Status:     string(api.StatusBuilding),
	}, nil
}

func (o *Orchestrator) doDeploy(ctx context.Context, req DeployRequest) (*DeployResult, error) {
	// 1. Validate input
	if err := validateName(req.Name); err != nil {
		return nil, err
	}
	if len(req.Files) == 0 {
		return nil, &api.ErrInvalidInput{Field: "files", Message: "at least one file is required"}
	}

	// 1b. Validate file paths (prevent path traversal)
	for path := range req.Files {
		if err := validateFilePath(path); err != nil {
			return nil, err
		}
	}

	// 1d. Validate port range (0 means "auto-detect", so only check explicit values)
	if req.Port != 0 && (req.Port < 1 || req.Port > 65535) {
		return nil, fmt.Errorf("invalid port %d: must be between 1 and 65535", req.Port)
	}

	// 1e. Validate and verify secret references
	for envName, ref := range req.SecretRefs {
		parts := strings.SplitN(ref, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid secret_ref %q for %s: must be in format 'secret-name:key'", ref, envName)
		}
		_, err := o.clientset.CoreV1().Secrets(o.cfg.Deployment.Namespace).Get(ctx, parts[0], metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("secret %q referenced by %s not found: %w", parts[0], envName, err)
		}
	}

	// 1c. Check for duplicate name — allow overwrite if stuck/failed
	if existing, _ := o.store.GetByName(ctx, req.Name); existing != nil {
		if existing.Status == api.StatusFailed || existing.Status == api.StatusBuilding {
			o.logger.Info("overwriting stuck/failed artifact with same name",
				"name", req.Name, "old_id", existing.ID, "old_status", existing.Status)
			_ = o.Delete(ctx, existing.ID)
		} else {
			return nil, &api.ErrAlreadyExists{Name: req.Name}
		}
	}

	// 2. Generate artifact ID
	artifactID := generateID()
	now := time.Now()

	artifact := &api.Artifact{
		ID:        artifactID,
		Name:      req.Name,
		OwnerID:   vibedauth.UserIDFromContext(ctx),
		Status:    api.StatusPending,
		Language:  req.Language,
		EnvVars:    req.EnvVars,
		SecretRefs: req.SecretRefs,
		Port:       req.Port,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	// 3. Create artifact record
	if err := o.store.Create(ctx, artifact); err != nil {
		return nil, err
	}

	// 4. Store source files
	o.updateStatus(ctx, artifact, api.StatusBuilding)
	storageRef, err := o.storage.StoreSource(ctx, artifactID, req.Files)
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("storing source: %v", err))
		return nil, fmt.Errorf("storing source: %w", err)
	}
	artifact.StorageRef = storageRef.LocalPath

	// 5. Detect language early (needed for target selection and go.mod generation)
	lang := req.Language
	if lang == "" || lang == "auto" {
		lang = builder.DetectLanguage(req.Files)
	}
	artifact.Language = lang

	// 6. Select deployment target (language-aware)
	preferred := api.DeploymentTarget(req.Target)
	if preferred == "" {
		preferred = api.DeploymentTarget(o.cfg.Deployment.PreferredTarget)
	}

	target, err := o.detector.SelectTarget(preferred)
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("selecting target: %v", err))
		return nil, err
	}
	artifact.Target = target

	// Static shortcut: skip build, use ConfigMap + nginx directly.
	if lang == "static" && isSmallStatic(req.Files) {
		o.logger.Info("using static ConfigMap deploy (skipping build)",
			"name", req.Name, "files", len(req.Files))
		return o.deployStatic(ctx, artifact, req.Files, target)
	}

	// 7. Build image (container or wasm depending on target)
	imageName := fmt.Sprintf("%s/%s:latest", o.imageBase, req.Name)

	o.metrics.BuildsInFlight.Inc()
	defer o.metrics.BuildsInFlight.Dec()
	buildStart := time.Now()

	activeBuilder := o.builder

	// Child span: builder.Build (local image, no push)
	buildCtx, buildSpan := o.tracer.Start(ctx, "builder.Build",
		trace.WithAttributes(
			attribute.String("builder.image", imageName),
			attribute.String("builder.language", lang),
		))
	// When the builder handles push internally (Buildah/Wasm K8s Job), set Publish: true
	// so the Job pushes to the registry. For Pack (local daemon), set Publish: false and
	// handle the push separately in the registry.Push span below.
	builderPublishes := activeBuilder.PublishesInternally()
	buildResult, err := activeBuilder.Build(buildCtx, builder.BuildRequest{
		SourceDir: storageRef.LocalPath,
		ImageName: imageName,
		Language:  lang,
		Env:       req.EnvVars,
		Publish:   builderPublishes && o.cfg.Registry.Enabled,
	})

	buildDur := time.Since(buildStart).Seconds()

	if err != nil {
		buildSpan.RecordError(err)
		buildSpan.SetStatus(codes.Error, err.Error())
		buildSpan.End()
		o.metrics.BuildsTotal.WithLabelValues("failed", lang).Inc()
		o.metrics.BuildDuration.WithLabelValues("failed", lang).Observe(buildDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("build failed: %v", err))
		return nil, &api.ErrBuildFailed{Reason: err.Error()}
	}
	buildSpan.End()

	o.metrics.BuildsTotal.WithLabelValues("success", lang).Inc()
	o.metrics.BuildDuration.WithLabelValues("success", lang).Observe(buildDur)
	artifact.ImageRef = buildResult.ImageRef

	// Child span: registry.Push — only needed when the builder doesn't push internally
	// (i.e. PackBuilder produces a local daemon image that must be pushed via crane).
	if o.cfg.Registry.Enabled && !builderPublishes {
		pushCtx, pushSpan := o.tracer.Start(ctx, "registry.Push",
			trace.WithAttributes(
				attribute.String("registry.image", buildResult.ImageRef),
				attribute.String("registry.url", o.cfg.Registry.URL),
			))
		if pushErr := pushImage(pushCtx, buildResult.ImageRef); pushErr != nil {
			pushSpan.RecordError(pushErr)
			pushSpan.SetStatus(codes.Error, pushErr.Error())
			pushSpan.End()
			o.failArtifact(ctx, artifact, fmt.Sprintf("push failed: %v", pushErr))
			return nil, fmt.Errorf("registry push: %w", pushErr)
		}
		pushSpan.End()
	}

	// 8. Deploy
	o.updateStatus(ctx, artifact, api.StatusDeploying)
	dep, err := o.factory.Get(target)
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("no deployer for target: %v", err))
		return nil, err
	}

	// Child span: deployer.Deploy
	deployCtx, deploySpan := o.tracer.Start(ctx, "deployer.Deploy",
		trace.WithAttributes(attribute.String("deployer.target", string(target))))
	deployStart := time.Now()
	deployResult, err := dep.Deploy(deployCtx, artifact)
	deployDur := time.Since(deployStart).Seconds()

	if err != nil {
		deploySpan.RecordError(err)
		deploySpan.SetStatus(codes.Error, err.Error())
		deploySpan.End()
		o.metrics.DeploysTotal.WithLabelValues("failed", string(target)).Inc()
		o.metrics.DeployDuration.WithLabelValues("failed", string(target)).Observe(deployDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("deploy failed: %v", err))
		return nil, &api.ErrDeployFailed{Reason: err.Error()}
	}
	deploySpan.End()

	o.metrics.DeploysTotal.WithLabelValues("success", string(target)).Inc()
	o.metrics.DeployDuration.WithLabelValues("success", string(target)).Observe(deployDur)
	o.metrics.ArtifactsActive.WithLabelValues(string(target)).Inc()

	// 9. Update artifact with URL, running status, and version
	artifact.URL = deployResult.URL
	artifact.Status = api.StatusRunning
	artifact.Version = 1
	artifact.VersionID = generateID()
	artifact.UpdatedAt = time.Now()
	if err := o.store.Update(ctx, artifact); err != nil {
		o.logger.Warn("failed to persist deploy result",
			"artifact_id", artifactID,
			"error", err,
		)
	}
	o.publishStatusEvent(artifact)

	// Create initial version snapshot
	o.createVersionSnapshot(ctx, artifact)

	o.logger.Info("artifact deployed successfully",
		"id", artifactID,
		"name", req.Name,
		"target", target,
		"url", deployResult.URL,
		"version", 1,
	)

	return &DeployResult{
		ArtifactID: artifactID,
		Name:       req.Name,
		URL:        deployResult.URL,
		Target:     string(target),
		Status:     string(api.StatusRunning),
		ImageRef:   buildResult.ImageRef,
	}, nil
}

// AsyncUpdate validates ownership synchronously, then runs the rebuild + redeploy in
// a background goroutine. Returns immediately with status "building" so MCP clients
// don't time out on long-running compilations.
func (o *Orchestrator) AsyncUpdate(ctx context.Context, req UpdateRequest) (*DeployResult, error) {
	// Ownership check must happen now, while we still have the authenticated context.
	artifact, err := o.store.Get(ctx, req.ArtifactID)
	if err != nil {
		return nil, err
	}
	if err := o.checkWriteOwnership(ctx, artifact); err != nil {
		return nil, err
	}

	// Capture identity before request context is cancelled.
	ownerID := vibedauth.UserIDFromContext(ctx)
	bgCtx := vibedauth.WithUserID(context.Background(), ownerID)

	bgCtx, span := o.tracer.Start(bgCtx, "orchestrator.Update",
		trace.WithAttributes(attribute.String("artifact.id", req.ArtifactID)))
	traceID := span.SpanContext().TraceID().String()
	o.logger.Info("async update started", "artifact_id", req.ArtifactID, "trace_id", traceID)

	// Mark as building synchronously so the caller can see progress immediately.
	o.updateStatus(ctx, artifact, api.StatusBuilding)

	go func() {
		defer span.End()
		result, err := o.doUpdate(bgCtx, req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			o.logger.Error("async update failed", "artifact_id", req.ArtifactID, "trace_id", traceID, "error", err)
		} else {
			span.SetStatus(codes.Ok, "")
			o.logger.Info("async update completed", "artifact_id", req.ArtifactID, "trace_id", traceID, "url", result.URL)
		}
	}()

	return &DeployResult{
		ArtifactID: req.ArtifactID,
		Name:       artifact.Name,
		Status:     string(api.StatusBuilding),
	}, nil
}

// Update rebuilds and redeploys an existing artifact.
func (o *Orchestrator) Update(ctx context.Context, req UpdateRequest) (*DeployResult, error) {
	ctx, span := o.tracer.Start(ctx, "orchestrator.Update",
		trace.WithAttributes(attribute.String("artifact.id", req.ArtifactID)))
	defer span.End()

	traceID := span.SpanContext().TraceID().String()
	o.logger.Info("update started", "artifact_id", req.ArtifactID, "trace_id", traceID)

	result, err := o.doUpdate(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		o.logger.Error("update failed", "artifact_id", req.ArtifactID, "trace_id", traceID, "error", err)
	} else {
		o.logger.Info("update completed", "artifact_id", req.ArtifactID, "trace_id", traceID)
	}
	return result, err
}

func (o *Orchestrator) doUpdate(ctx context.Context, req UpdateRequest) (*DeployResult, error) {
	artifact, err := o.store.Get(ctx, req.ArtifactID)
	if err != nil {
		return nil, err
	}

	if err := o.checkWriteOwnership(ctx, artifact); err != nil {
		return nil, err
	}

	// Validate file paths (prevent path traversal)
	for path := range req.Files {
		if err := validateFilePath(path); err != nil {
			return nil, err
		}
	}

	// Validate secret references
	for envName, ref := range req.SecretRefs {
		parts := strings.SplitN(ref, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid secret_ref %q for %s: must be in format 'secret-name:key'", ref, envName)
		}
		_, err := o.clientset.CoreV1().Secrets(o.cfg.Deployment.Namespace).Get(ctx, parts[0], metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("secret %q referenced by %s not found: %w", parts[0], envName, err)
		}
	}

	// Store new source
	o.updateStatus(ctx, artifact, api.StatusBuilding)
	storageRef, err := o.storage.StoreSource(ctx, artifact.ID, req.Files)
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("storing source: %v", err))
		return nil, fmt.Errorf("storing source: %w", err)
	}
	artifact.StorageRef = storageRef.LocalPath

	if req.EnvVars != nil {
		artifact.EnvVars = req.EnvVars
	}
	if req.SecretRefs != nil {
		artifact.SecretRefs = req.SecretRefs
	}

	// Detect language for static shortcut
	lang := artifact.Language
	if lang == "" || lang == "auto" {
		lang = builder.DetectLanguage(req.Files)
	}
	artifact.Language = lang

	// Static shortcut: update ConfigMap directly, skip build
	if lang == "static" && isSmallStatic(req.Files) {
		o.logger.Info("using static ConfigMap update (skipping build)", "name", artifact.Name)
		return o.updateStatic(ctx, artifact, req.Files)
	}

	// Rebuild (non-static path)
	imageName := fmt.Sprintf("%s/%s:latest", o.imageBase, artifact.Name)

	o.metrics.BuildsInFlight.Inc()
	defer o.metrics.BuildsInFlight.Dec()
	buildStart := time.Now()

	activeBuilder := o.builder

	// Child span: builder.Build (update path)
	builderPublishes := activeBuilder.PublishesInternally()
	buildCtx, buildSpan := o.tracer.Start(ctx, "builder.Build",
		trace.WithAttributes(attribute.String("builder.image", imageName), attribute.String("builder.language", lang)))
	buildResult, err := activeBuilder.Build(buildCtx, builder.BuildRequest{
		SourceDir: storageRef.LocalPath,
		ImageName: imageName,
		Language:  lang,
		Env:       artifact.EnvVars,
		Publish:   builderPublishes && o.cfg.Registry.Enabled,
	})

	buildDur := time.Since(buildStart).Seconds()

	if err != nil {
		buildSpan.RecordError(err)
		buildSpan.SetStatus(codes.Error, err.Error())
		buildSpan.End()
		o.metrics.BuildsTotal.WithLabelValues("failed", lang).Inc()
		o.metrics.BuildDuration.WithLabelValues("failed", lang).Observe(buildDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("build failed: %v", err))
		return nil, &api.ErrBuildFailed{Reason: err.Error()}
	}
	buildSpan.End()

	o.metrics.BuildsTotal.WithLabelValues("success", lang).Inc()
	o.metrics.BuildDuration.WithLabelValues("success", lang).Observe(buildDur)
	artifact.ImageRef = buildResult.ImageRef

	// Child span: registry.Push (only for Pack — Buildah/Wasm push inside the K8s Job)
	if o.cfg.Registry.Enabled && !builderPublishes {
		pushCtx, pushSpan := o.tracer.Start(ctx, "registry.Push",
			trace.WithAttributes(
				attribute.String("registry.image", buildResult.ImageRef),
				attribute.String("registry.url", o.cfg.Registry.URL),
			))
		if pushErr := pushImage(pushCtx, buildResult.ImageRef); pushErr != nil {
			pushSpan.RecordError(pushErr)
			pushSpan.SetStatus(codes.Error, pushErr.Error())
			pushSpan.End()
			o.failArtifact(ctx, artifact, fmt.Sprintf("push failed: %v", pushErr))
			return nil, fmt.Errorf("registry push: %w", pushErr)
		}
		pushSpan.End()
	}

	// Redeploy
	o.updateStatus(ctx, artifact, api.StatusDeploying)
	dep, err := o.factory.Get(artifact.Target)
	if err != nil {
		return nil, err
	}

	// Child span: deployer.Update
	target := string(artifact.Target)
	deployCtx, deploySpan := o.tracer.Start(ctx, "deployer.Update",
		trace.WithAttributes(attribute.String("deployer.target", target)))
	deployStart := time.Now()
	deployResult, err := dep.Update(deployCtx, artifact)
	deployDur := time.Since(deployStart).Seconds()

	if err != nil {
		deploySpan.RecordError(err)
		deploySpan.SetStatus(codes.Error, err.Error())
		deploySpan.End()
		o.metrics.DeploysTotal.WithLabelValues("failed", target).Inc()
		o.metrics.DeployDuration.WithLabelValues("failed", target).Observe(deployDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("deploy failed: %v", err))
		return nil, &api.ErrDeployFailed{Reason: err.Error()}
	}
	deploySpan.End()

	o.metrics.DeploysTotal.WithLabelValues("success", target).Inc()
	o.metrics.DeployDuration.WithLabelValues("success", target).Observe(deployDur)

	newVersion := artifact.Version + 1
	if newVersion <= 1 {
		newVersion = 2 // pre-versioning artifacts jump from 0 to 2
	}
	artifact.URL = deployResult.URL
	artifact.Status = api.StatusRunning
	artifact.Version = newVersion
	artifact.VersionID = generateID()
	artifact.UpdatedAt = time.Now()
	if err := o.store.Update(ctx, artifact); err != nil {
		o.logger.Warn("failed to persist update result",
			"artifact_id", artifact.ID,
			"error", err,
		)
	}
	o.publishStatusEvent(artifact)

	// Create version snapshot
	o.createVersionSnapshot(ctx, artifact)

	return &DeployResult{
		ArtifactID: artifact.ID,
		Name:       artifact.Name,
		URL:        deployResult.URL,
		Target:     target,
		Status:     string(api.StatusRunning),
		ImageRef:   buildResult.ImageRef,
	}, nil
}

// Delete stops and removes a deployed artifact.
func (o *Orchestrator) Delete(ctx context.Context, artifactID string) error {
	ctx, span := o.tracer.Start(ctx, "orchestrator.Delete",
		trace.WithAttributes(attribute.String("artifact.id", artifactID)))
	defer span.End()

	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		o.metrics.DeletesTotal.WithLabelValues("failed").Inc()
		return err
	}

	if err := o.checkWriteOwnership(ctx, artifact); err != nil {
		o.metrics.DeletesTotal.WithLabelValues("failed").Inc()
		return err
	}

	// Skip deployer cleanup if artifact never got a target (e.g. stuck in building)
	if artifact.Target != "" {
		dep, err := o.factory.Get(artifact.Target)
		if err != nil {
			o.logger.Warn("failed to get deployer for delete", "id", artifactID, "target", artifact.Target, "error", err)
		} else if err := dep.Delete(ctx, artifact); err != nil {
			o.logger.Warn("failed to delete deployment", "id", artifactID, "error", err)
		}
	}

	if err := o.storage.Delete(ctx, artifactID); err != nil {
		o.logger.Warn("failed to delete storage", "id", artifactID, "error", err)
	}

	if err := o.store.Delete(ctx, artifactID); err != nil {
		o.metrics.DeletesTotal.WithLabelValues("failed").Inc()
		return err
	}

	o.metrics.DeletesTotal.WithLabelValues("success").Inc()
	o.metrics.ArtifactsActive.WithLabelValues(string(artifact.Target)).Dec()
	o.publishDeleteEvent(artifactID)
	return nil
}

// Status returns detailed status for an artifact.
func (o *Orchestrator) Status(ctx context.Context, artifactID string) (*api.Artifact, error) {
	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if err := o.checkOwnership(ctx, artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

// List returns artifacts matching the filter with pagination, scoped to the authenticated user.
func (o *Orchestrator) List(ctx context.Context, statusFilter string, offset, limit int) (*store.ListResult, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return o.store.List(ctx, store.ListOptions{
		StatusFilter: statusFilter,
		OwnerID:      vibedauth.UserIDFromContext(ctx),
		AdminView:    vibedauth.IsAdmin(ctx),
		Offset:       offset,
		Limit:        limit,
	})
}

// Logs returns recent log lines from a deployed artifact.
func (o *Orchestrator) Logs(ctx context.Context, artifactID string, lines int) ([]string, error) {
	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return nil, err
	}

	if err := o.checkOwnership(ctx, artifact); err != nil {
		return nil, err
	}

	if lines <= 0 {
		lines = 50
	}

	dep, err := o.factory.Get(artifact.Target)
	if err != nil {
		return nil, err
	}

	return dep.GetLogs(ctx, artifact, lines)
}

// ListTargets returns info about available deployment targets.
func (o *Orchestrator) ListTargets() []api.TargetInfo {
	return o.detector.ListTargets()
}

// checkOwnership verifies that the current user can read the artifact.
// Allows: owner, admin, or users in the SharedWith list.
// Returns ErrNotFound (not Forbidden) to avoid leaking artifact existence to non-owners.
// When auth is disabled (ownerID is empty), all ownership checks pass.
func (o *Orchestrator) checkOwnership(ctx context.Context, artifact *api.Artifact) error {
	ownerID := vibedauth.UserIDFromContext(ctx)
	if ownerID == "" {
		return nil // Auth disabled — no ownership enforcement
	}
	if vibedauth.IsAdmin(ctx) {
		return nil // Admin can access everything
	}
	if artifact.OwnerID == ownerID {
		return nil // Owner match
	}
	// Check shared access
	for _, uid := range artifact.SharedWith {
		if uid == ownerID {
			return nil
		}
	}
	return &api.ErrNotFound{ArtifactID: artifact.ID}
}

// checkWriteOwnership verifies that the current user can modify the artifact.
// Only owner and admin can write — shared users have read-only access.
func (o *Orchestrator) checkWriteOwnership(ctx context.Context, artifact *api.Artifact) error {
	ownerID := vibedauth.UserIDFromContext(ctx)
	if ownerID == "" {
		return nil // Auth disabled
	}
	if vibedauth.IsAdmin(ctx) {
		return nil // Admin can modify everything
	}
	if artifact.OwnerID != "" && artifact.OwnerID != ownerID {
		return &api.ErrNotFound{ArtifactID: artifact.ID}
	}
	return nil
}

func (o *Orchestrator) updateStatus(ctx context.Context, artifact *api.Artifact, status api.ArtifactStatus) {
	artifact.Status = status
	artifact.UpdatedAt = time.Now()
	if err := o.store.Update(ctx, artifact); err != nil {
		o.logger.Warn("failed to persist status update",
			"artifact_id", artifact.ID,
			"status", status,
			"error", err,
		)
	}
	o.publishStatusEvent(artifact)
}

func (o *Orchestrator) failArtifact(_ context.Context, artifact *api.Artifact, reason string) {
	failCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	artifact.Status = api.StatusFailed
	artifact.Error = reason
	artifact.UpdatedAt = time.Now()
	if err := o.store.Update(failCtx, artifact); err != nil {
		o.logger.Warn("failed to persist artifact failure",
			"artifact_id", artifact.ID,
			"reason", reason,
			"error", err,
		)
	}
	o.publishStatusEvent(artifact)
}

// publishStatusEvent publishes an artifact lifecycle event to the event bus.
func (o *Orchestrator) publishStatusEvent(artifact *api.Artifact) {
	if o.events == nil {
		return
	}
	o.events.Publish(events.Event{
		Type:       events.ArtifactStatusChanged,
		ArtifactID: artifact.ID,
		OwnerID:    artifact.OwnerID,
		Status:     string(artifact.Status),
		Error:      artifact.Error,
		Timestamp:  artifact.UpdatedAt,
	})
}

// publishDeleteEvent publishes an artifact deletion event to the event bus.
func (o *Orchestrator) publishDeleteEvent(artifactID string) {
	if o.events == nil {
		return
	}
	o.events.Publish(events.Event{
		Type:       events.ArtifactDeleted,
		ArtifactID: artifactID,
		Timestamp:  time.Now(),
	})
}

func validateName(name string) error {
	if name == "" {
		return &api.ErrInvalidInput{Field: "name", Message: "name is required"}
	}
	if len(name) > 63 {
		return &api.ErrInvalidInput{Field: "name", Message: "name must be 63 characters or less"}
	}
	lower := strings.ToLower(name)
	if !dnsNameRegex.MatchString(lower) {
		return &api.ErrInvalidInput{
			Field:   "name",
			Message: "name must be lowercase alphanumeric with hyphens (DNS-safe)",
		}
	}
	return nil
}

// validateFilePath rejects file paths that could escape the artifact directory.
func validateFilePath(path string) error {
	if path == "" {
		return &api.ErrInvalidInput{Field: "files", Message: "file path cannot be empty"}
	}
	if filepath.IsAbs(path) {
		return &api.ErrInvalidInput{Field: "files", Message: fmt.Sprintf("absolute paths not allowed: %q", path)}
	}
	if strings.Contains(path, "\\") {
		return &api.ErrInvalidInput{Field: "files", Message: fmt.Sprintf("backslashes not allowed in path: %q", path)}
	}
	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, "..") {
		return &api.ErrInvalidInput{Field: "files", Message: fmt.Sprintf("path traversal not allowed: %q", path)}
	}
	return nil
}

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

const staticNginxConf = `server {
    listen 8080;
    server_name _;
    root /usr/share/nginx/html;
    index index.html index.htm;
    location / {
        try_files $uri $uri/ /index.html;
    }
}
`

// isSmallStatic returns true if total file content fits in a ConfigMap (< 900KB).
func isSmallStatic(files map[string]string) bool {
	total := 0
	for _, content := range files {
		total += len(content)
	}
	return total < 900*1024
}

// deployStatic creates a ConfigMap with files + nginx config, then deploys nginx directly.
func (o *Orchestrator) deployStatic(ctx context.Context, artifact *api.Artifact, files map[string]string, target api.DeploymentTarget) (*DeployResult, error) {
	cmName := fmt.Sprintf("vibed-static-%s", artifact.Name)
	ns := o.cfg.Deployment.Namespace

	// Build ConfigMap data: user files + nginx config
	data := make(map[string]string, len(files)+1)
	for k, v := range files {
		data[k] = v
	}
	data["nginx.conf"] = staticNginxConf

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "vibed",
				"vibed.dev/artifact-id":        artifact.ID,
			},
		},
		Data: data,
	}

	_, err := o.clientset.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("creating static ConfigMap: %v", err))
		return nil, fmt.Errorf("creating static ConfigMap: %w", err)
	}

	// Set artifact fields for static deploy
	artifact.ImageRef = "nginx:alpine"
	artifact.StaticFiles = cmName
	if artifact.Port == 0 {
		artifact.Port = 8080
	}

	// Deploy
	o.updateStatus(ctx, artifact, api.StatusDeploying)
	dep, err := o.factory.Get(target)
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("no deployer for target: %v", err))
		return nil, err
	}

	deployStart := time.Now()
	deployResult, err := dep.Deploy(ctx, artifact)
	deployDur := time.Since(deployStart).Seconds()

	if err != nil {
		o.metrics.DeploysTotal.WithLabelValues("failed", string(target)).Inc()
		o.metrics.DeployDuration.WithLabelValues("failed", string(target)).Observe(deployDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("deploy failed: %v", err))
		return nil, &api.ErrDeployFailed{Reason: err.Error()}
	}

	o.metrics.DeploysTotal.WithLabelValues("success", string(target)).Inc()
	o.metrics.DeployDuration.WithLabelValues("success", string(target)).Observe(deployDur)
	o.metrics.ArtifactsActive.WithLabelValues(string(target)).Inc()

	artifact.URL = deployResult.URL
	artifact.Status = api.StatusRunning
	artifact.Version = 1
	artifact.VersionID = generateID()
	artifact.UpdatedAt = time.Now()
	if err := o.store.Update(ctx, artifact); err != nil {
		o.logger.Warn("failed to persist static deploy result",
			"artifact_id", artifact.ID,
			"error", err,
		)
	}
	o.publishStatusEvent(artifact)

	o.createVersionSnapshot(ctx, artifact)

	o.logger.Info("static artifact deployed (no build)",
		"id", artifact.ID, "name", artifact.Name,
		"target", target, "url", deployResult.URL,
		"version", 1)

	return &DeployResult{
		ArtifactID: artifact.ID,
		Name:       artifact.Name,
		URL:        deployResult.URL,
		Target:     string(target),
		Status:     string(api.StatusRunning),
		ImageRef:   "nginx:alpine",
	}, nil
}

// updateStatic replaces the ConfigMap and triggers a redeployment for static artifacts.
func (o *Orchestrator) updateStatic(ctx context.Context, artifact *api.Artifact, files map[string]string) (*DeployResult, error) {
	cmName := artifact.StaticFiles
	if cmName == "" {
		cmName = fmt.Sprintf("vibed-static-%s", artifact.Name)
	}
	ns := o.cfg.Deployment.Namespace

	// Build new ConfigMap data
	data := make(map[string]string, len(files)+1)
	for k, v := range files {
		data[k] = v
	}
	data["nginx.conf"] = staticNginxConf

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "vibed",
				"vibed.dev/artifact-id":        artifact.ID,
			},
		},
		Data: data,
	}

	// Replace ConfigMap (delete + create for clean update)
	_ = o.clientset.CoreV1().ConfigMaps(ns).Delete(ctx, cmName, metav1.DeleteOptions{})
	_, err := o.clientset.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("updating static ConfigMap: %v", err))
		return nil, fmt.Errorf("updating static ConfigMap: %w", err)
	}

	artifact.ImageRef = "nginx:alpine"
	artifact.StaticFiles = cmName
	if artifact.Port == 0 {
		artifact.Port = 8080
	}

	// Redeploy
	o.updateStatus(ctx, artifact, api.StatusDeploying)
	dep, err := o.factory.Get(artifact.Target)
	if err != nil {
		return nil, err
	}

	target := string(artifact.Target)
	deployStart := time.Now()
	deployResult, err := dep.Update(ctx, artifact)
	deployDur := time.Since(deployStart).Seconds()

	if err != nil {
		o.metrics.DeploysTotal.WithLabelValues("failed", target).Inc()
		o.metrics.DeployDuration.WithLabelValues("failed", target).Observe(deployDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("deploy failed: %v", err))
		return nil, &api.ErrDeployFailed{Reason: err.Error()}
	}

	o.metrics.DeploysTotal.WithLabelValues("success", target).Inc()
	o.metrics.DeployDuration.WithLabelValues("success", target).Observe(deployDur)

	newVersion := artifact.Version + 1
	if newVersion <= 1 {
		newVersion = 2
	}
	artifact.URL = deployResult.URL
	artifact.Status = api.StatusRunning
	artifact.Version = newVersion
	artifact.VersionID = generateID()
	artifact.UpdatedAt = time.Now()
	if err := o.store.Update(ctx, artifact); err != nil {
		o.logger.Warn("failed to persist static update result",
			"artifact_id", artifact.ID,
			"error", err,
		)
	}
	o.publishStatusEvent(artifact)

	o.createVersionSnapshot(ctx, artifact)

	return &DeployResult{
		ArtifactID: artifact.ID,
		Name:       artifact.Name,
		URL:        deployResult.URL,
		Target:     target,
		Status:     string(api.StatusRunning),
		ImageRef:   "nginx:alpine",
	}, nil
}

// createVersionSnapshot stores a point-in-time version snapshot of the artifact.
func (o *Orchestrator) createVersionSnapshot(ctx context.Context, artifact *api.Artifact) {
	version := &api.ArtifactVersion{
		VersionID:  artifact.VersionID,
		ArtifactID: artifact.ID,
		Version:    artifact.Version,
		ImageRef:   artifact.ImageRef,
		StorageRef: artifact.StorageRef,
		EnvVars:    artifact.EnvVars,
		SecretRefs: artifact.SecretRefs,
		Status:     artifact.Status,
		URL:        artifact.URL,
		CreatedAt:  artifact.UpdatedAt,
		CreatedBy:  vibedauth.UserIDFromContext(ctx),
	}

	if err := o.store.CreateVersion(ctx, version); err != nil {
		o.logger.Warn("failed to create version snapshot",
			"artifact_id", artifact.ID,
			"version", artifact.Version,
			"error", err,
		)
	}
}

// ListVersions returns all version snapshots for an artifact.
func (o *Orchestrator) ListVersions(ctx context.Context, artifactID string) ([]api.ArtifactVersion, error) {
	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if err := o.checkOwnership(ctx, artifact); err != nil {
		return nil, err
	}
	return o.store.ListVersions(ctx, artifactID)
}

// Rollback redeploys an artifact using a previous version's image and config.
// It creates a new version entry for the rollback (does not rewind history).
func (o *Orchestrator) Rollback(ctx context.Context, artifactID string, targetVersion int) (*DeployResult, error) {
	ctx, span := o.tracer.Start(ctx, "orchestrator.Rollback",
		trace.WithAttributes(
			attribute.String("artifact.id", artifactID),
			attribute.Int("target_version", targetVersion)))
	defer span.End()

	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if err := o.checkWriteOwnership(ctx, artifact); err != nil {
		return nil, err
	}

	// Load the target version snapshot
	snapshot, err := o.store.GetVersion(ctx, artifactID, targetVersion)
	if err != nil {
		return nil, err
	}

	// Apply the snapshot's image and env vars to the current artifact
	artifact.ImageRef = snapshot.ImageRef
	artifact.StorageRef = snapshot.StorageRef
	if snapshot.EnvVars != nil {
		artifact.EnvVars = snapshot.EnvVars
	}
	if snapshot.SecretRefs != nil {
		artifact.SecretRefs = snapshot.SecretRefs
	}

	// Redeploy with the old image
	o.updateStatus(ctx, artifact, api.StatusDeploying)
	dep, err := o.factory.Get(artifact.Target)
	if err != nil {
		return nil, err
	}

	target := string(artifact.Target)
	deployStart := time.Now()
	deployResult, err := dep.Update(ctx, artifact)
	deployDur := time.Since(deployStart).Seconds()

	if err != nil {
		o.metrics.DeploysTotal.WithLabelValues("failed", target).Inc()
		o.metrics.DeployDuration.WithLabelValues("failed", target).Observe(deployDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("rollback deploy failed: %v", err))
		return nil, &api.ErrDeployFailed{Reason: fmt.Sprintf("rollback to v%d failed: %v", targetVersion, err)}
	}

	o.metrics.DeploysTotal.WithLabelValues("success", target).Inc()
	o.metrics.DeployDuration.WithLabelValues("success", target).Observe(deployDur)

	// Create a new version entry for the rollback
	newVersion := artifact.Version + 1
	if newVersion <= 1 {
		newVersion = 2
	}
	artifact.URL = deployResult.URL
	artifact.Status = api.StatusRunning
	artifact.Version = newVersion
	artifact.VersionID = generateID()
	artifact.UpdatedAt = time.Now()
	if err := o.store.Update(ctx, artifact); err != nil {
		o.logger.Warn("failed to persist rollback result",
			"artifact_id", artifact.ID,
			"error", err,
		)
	}
	o.publishStatusEvent(artifact)

	o.createVersionSnapshot(ctx, artifact)

	o.logger.Info("artifact rolled back",
		"id", artifactID,
		"from_version", artifact.Version-1,
		"to_snapshot", targetVersion,
		"new_version", newVersion,
	)

	return &DeployResult{
		ArtifactID: artifact.ID,
		Name:       artifact.Name,
		URL:        deployResult.URL,
		Target:     target,
		Status:     string(api.StatusRunning),
		ImageRef:   artifact.ImageRef,
	}, nil
}

// ShareArtifact grants read-only access to the specified users.
// Only the owner or an admin can share an artifact.
func (o *Orchestrator) ShareArtifact(ctx context.Context, artifactID string, userIDs []string) error {
	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return err
	}
	if err := o.checkWriteOwnership(ctx, artifact); err != nil {
		return err
	}

	// Merge and deduplicate
	existing := make(map[string]bool, len(artifact.SharedWith))
	for _, uid := range artifact.SharedWith {
		existing[uid] = true
	}
	for _, uid := range userIDs {
		if uid != "" && !existing[uid] {
			artifact.SharedWith = append(artifact.SharedWith, uid)
			existing[uid] = true
		}
	}
	sort.Strings(artifact.SharedWith)

	artifact.UpdatedAt = time.Now()
	return o.store.Update(ctx, artifact)
}

// UnshareArtifact revokes read-only access from the specified users.
// Only the owner or an admin can unshare an artifact.
func (o *Orchestrator) UnshareArtifact(ctx context.Context, artifactID string, userIDs []string) error {
	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return err
	}
	if err := o.checkWriteOwnership(ctx, artifact); err != nil {
		return err
	}

	// Build removal set
	toRemove := make(map[string]bool, len(userIDs))
	for _, uid := range userIDs {
		toRemove[uid] = true
	}

	// Filter out removed users
	filtered := make([]string, 0, len(artifact.SharedWith))
	for _, uid := range artifact.SharedWith {
		if !toRemove[uid] {
			filtered = append(filtered, uid)
		}
	}
	artifact.SharedWith = filtered

	artifact.UpdatedAt = time.Now()
	return o.store.Update(ctx, artifact)
}

// --- Share Link methods ---

// CreateShareLink generates a public share link for an artifact.
func (o *Orchestrator) CreateShareLink(ctx context.Context, artifactID, password string, expiresIn time.Duration) (*api.ShareLink, error) {
	if o.shareLinkStore == nil {
		return nil, fmt.Errorf("share links require SQLite store backend")
	}

	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if err := o.checkWriteOwnership(ctx, artifact); err != nil {
		return nil, err
	}

	// Generate 32-byte crypto-random token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generating token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	// Hash password if provided
	var passwordHash string
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("hashing password: %w", err)
		}
		passwordHash = string(hash)
	}

	now := time.Now()
	link := &api.ShareLink{
		Token:       token,
		ArtifactID:  artifactID,
		CreatedBy:   vibedauth.UserIDFromContext(ctx),
		HasPassword: password != "",
		CreatedAt:   now,
	}
	if expiresIn > 0 {
		exp := now.Add(expiresIn)
		link.ExpiresAt = &exp
	}
	if o.cfg.Server.BaseURL != "" {
		link.URL = o.cfg.Server.BaseURL + "/share/" + token
	}

	if err := o.shareLinkStore.CreateShareLink(ctx, link, passwordHash); err != nil {
		return nil, err
	}

	return link, nil
}

// ListShareLinks returns all share links for an artifact.
func (o *Orchestrator) ListShareLinks(ctx context.Context, artifactID string) ([]api.ShareLink, error) {
	if o.shareLinkStore == nil {
		return nil, fmt.Errorf("share links require SQLite store backend")
	}

	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if err := o.checkOwnership(ctx, artifact); err != nil {
		return nil, err
	}
	links, err := o.shareLinkStore.ListShareLinks(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if o.cfg.Server.BaseURL != "" {
		for i := range links {
			links[i].URL = o.cfg.Server.BaseURL + "/share/" + links[i].Token
		}
	}
	return links, nil
}

// RevokeShareLink revokes a share link.
func (o *Orchestrator) RevokeShareLink(ctx context.Context, token string) error {
	if o.shareLinkStore == nil {
		return fmt.Errorf("share links require SQLite store backend")
	}

	link, _, err := o.shareLinkStore.GetShareLink(ctx, token)
	if err != nil {
		return err
	}
	artifact, err := o.store.Get(ctx, link.ArtifactID)
	if err != nil {
		return err
	}
	if err := o.checkWriteOwnership(ctx, artifact); err != nil {
		return err
	}
	return o.shareLinkStore.RevokeShareLink(ctx, token)
}

// ResolveShareLink validates a share link token and optional password, returns read-only artifact view.
// pushImage pushes a locally built image (in the container daemon) to the registry
// using go-containerregistry. It is called after builder.Build when registry is enabled
// so that the push step can be traced as a dedicated child span.
func pushImage(ctx context.Context, imageRef string) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parse image ref %q: %w", imageRef, err)
	}
	img, err := daemon.Image(ref, daemon.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("load image from daemon %q: %w", imageRef, err)
	}
	if err := crane.Push(img, imageRef, crane.WithContext(ctx)); err != nil {
		return fmt.Errorf("push image %q: %w", imageRef, err)
	}
	return nil
}

func (o *Orchestrator) ResolveShareLink(ctx context.Context, token, password string) (*api.Artifact, error) {
	if o.shareLinkStore == nil {
		return nil, fmt.Errorf("share links require SQLite store backend")
	}

	link, passwordHash, err := o.shareLinkStore.GetShareLink(ctx, token)
	if err != nil {
		return nil, err
	}

	// Revoked or expired links return not-found (no information leakage)
	if link.Revoked {
		return nil, &api.ErrShareLinkNotFound{Token: token}
	}
	if link.ExpiresAt != nil && time.Now().After(*link.ExpiresAt) {
		return nil, &api.ErrShareLinkNotFound{Token: token}
	}

	// Check password
	if passwordHash != "" {
		if password == "" {
			return nil, &api.ErrPasswordRequired{}
		}
		if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
			return nil, &api.ErrPasswordRequired{}
		}
	}

	return o.store.Get(ctx, link.ArtifactID)
}
