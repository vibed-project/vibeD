package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sync/singleflight"

	"github.com/vibed-project/vibeD/internal/appspec"
	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/builder"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/environment"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/prebaked"
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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var dnsNameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// DeployRequest is the input for deploying a new artifact.
type DeployRequest struct {
        Name         string
        Files        map[string]string
        Language     string
        Target       string
        EnvVars      map[string]string
        SecretRefs   map[string]string // env var name → "secret-name:key"
        Port         int
        OwnerID      string
        DepartmentID string
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
	cfg            *config.Config
	detector       *environment.Detector
	builder        builder.Builder
	factory        *deployer.Factory
	storage        storage.Storage
	store          store.ArtifactStore
	userStore      store.UserStore
	metrics        *metrics.Metrics
	clientset      kubernetes.Interface
	events         *events.EventBus
	shareLinkStore store.ShareLinkStore
	imageBase      string
	tracer         trace.Tracer
	logger         *slog.Logger

	// lifeCtx is cancelled on orchestrator Shutdown — async goroutines derive
	// their context from this so SIGTERM unblocks in-flight deploys instead of
	// leaking them. Set via SetLifecycleContext (called from main.go).
	lifeCtx context.Context

	// deployFlight collapses concurrent deploys for the same artifact name.
	// MCP clients on flaky networks otherwise spawn multiple parallel
	// build+deploy chains that race each other to write the store.
	deployFlight singleflight.Group
}

// NewOrchestrator creates a new Orchestrator with all subsystems wired.
func NewOrchestrator(
	cfg *config.Config,
	detector *environment.Detector,
	bldr builder.Builder,
	factory *deployer.Factory,
	stg storage.Storage,
	st store.ArtifactStore,
	userStore store.UserStore,
	m *metrics.Metrics,	clientset kubernetes.Interface,
	bus *events.EventBus,
	shareLinkStore store.ShareLinkStore,
	logger *slog.Logger,
) *Orchestrator {
	imageBase := "vibed-artifacts"
	if cfg.Registry.Enabled && cfg.Registry.URL != "" {
		imageBase = cfg.Registry.URL
	}

	return &Orchestrator{
		cfg:            cfg,
		detector:       detector,
		builder:        bldr,
		factory:        factory,
		storage:        stg,
		store:          st,
		userStore:      userStore,
		metrics:        m,		clientset:      clientset,
		events:         bus,
		shareLinkStore: shareLinkStore,
		imageBase:      imageBase,
		tracer:         otel.Tracer("vibed/orchestrator"),
		logger:         logger,
		// Default to context.Background() until SetLifecycleContext is called;
		// callers that don't use Shutdown semantics still get correct behavior.
		lifeCtx: context.Background(),
	}
}

// SetLifecycleContext wires a long-lived context into the orchestrator.
// Async goroutines (AsyncDeploy, AsyncUpdate) derive from this context so
// SIGTERM cancels in-flight builds instead of leaving them orphaned.
func (o *Orchestrator) SetLifecycleContext(ctx context.Context) {
	o.lifeCtx = ctx
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

	result, err := o.doDeploy(ctx, req, "")
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
//
// Concurrent calls with the same artifact name are collapsed via singleflight:
// the second caller gets the same DeployResult as the first (same artifact_id),
// so MCP retries on flaky networks don't spawn parallel build chains.
func (o *Orchestrator) AsyncDeploy(ctx context.Context, req DeployRequest) (*DeployResult, error) {
	// Fast pre-flight checks so callers get immediate errors for bad input.
	if err := validateName(req.Name); err != nil {
		return nil, err
	}
	if len(req.Files) == 0 {
		return nil, &api.ErrInvalidInput{Field: "files", Message: "at least one file is required"}
	}

	// Collapse duplicate concurrent deploys for the same name. The actual
	// pre-flight + goroutine spawn happens inside Do; subsequent callers
	// while the first is still running get the same DeployResult.
	v, err, _ := o.deployFlight.Do("deploy:"+req.Name, func() (interface{}, error) {
		return o.asyncDeployImpl(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	return v.(*DeployResult), nil
}

func (o *Orchestrator) asyncDeployImpl(ctx context.Context, req DeployRequest) (*DeployResult, error) {

	// Detect language so doDeploy doesn't re-detect (avoids double-scanning large file maps).
	lang := req.Language
	if lang == "" || lang == "auto" {
		lang = appspec.DetectLanguage(req.Files)
	}
	req.Language = lang

	// Capture user identity before the request context is cancelled.
	ownerID := vibedauth.UserIDFromContext(ctx)
	bgCtx := vibedauth.WithUserID(o.lifeCtx, ownerID)

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

	// Run the full deploy in the background, telling doDeploy to operate on the
	// pre-created record by ID (no GetByName overwrite, no second store.Create).
	// The deferred recover ensures a panic anywhere in the deploy pipeline
	// flips the artifact to "failed" instead of leaving it stuck in
	// "building" until the GC catches it.
	go func() {
		defer span.End()
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				o.logger.Error("async deploy panicked", "artifact", req.Name, "trace_id", traceID, "panic", r, "stack", stack)
				if a, _ := o.store.Get(o.lifeCtx, artifactID); a != nil {
					o.failArtifact(o.lifeCtx, a, fmt.Sprintf("panic during deploy: %v", r))
				}
				span.RecordError(fmt.Errorf("panic: %v", r))
				span.SetStatus(codes.Error, "panic")
			}
		}()
		result, err := o.doDeploy(bgCtx, req, artifactID)
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

// doDeploy runs the full validate → store → build → deploy pipeline.
//
// When prebuiltID is non-empty, the caller (AsyncDeploy) has already created
// the artifact record synchronously and we operate on that ID instead of
// generating a new one and overwriting via GetByName. This prevents the
// double-create bug where the pre-created ID returned to the caller was
// invalidated mid-flight.
func (o *Orchestrator) doDeploy(ctx context.Context, req DeployRequest, prebuiltID string) (*DeployResult, error) {
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

	// 1c. Check for duplicate name — allow overwrite if stuck/failed.
	// Skip when prebuiltID is set: AsyncDeploy already created our record and
	// the GetByName below would otherwise find and delete it.
	if prebuiltID == "" {
		if existing, _ := o.store.GetByName(ctx, req.Name); existing != nil {
			if existing.Status == api.StatusFailed || existing.Status == api.StatusBuilding {
				o.logger.Info("overwriting stuck/failed artifact with same name",
					"name", req.Name, "old_id", existing.ID, "old_status", existing.Status)
				_ = o.Delete(ctx, existing.ID)
			} else {
				return nil, &api.ErrAlreadyExists{Name: req.Name}
			}
		}
	}

	// 2. Resolve / create artifact record
	now := time.Now()
	namespace := o.cfg.Deployment.Namespace // fallback
	if req.DepartmentID != "" && o.userStore != nil {
	        dept, err := o.userStore.GetDepartment(ctx, req.DepartmentID)
	        if err == nil && dept.Namespace != "" {
	                namespace = dept.Namespace
	        }
	}

	var artifact *api.Artifact
	var artifactID string
	if prebuiltID != "" {
		// AsyncDeploy path: fetch the record it pre-created so we can keep
		// updating it in place rather than deleting and recreating.
		existing, err := o.store.Get(ctx, prebuiltID)
		if err != nil {
			return nil, fmt.Errorf("prebuilt artifact %s not found: %w", prebuiltID, err)
		}
		artifact = existing
		artifactID = prebuiltID
		// Fill in fields that AsyncDeploy didn't know yet.
		artifact.Namespace = namespace
		artifact.Language = req.Language
		artifact.UpdatedAt = now
	} else {
		artifactID = generateID()
		artifact = &api.Artifact{
			ID:         artifactID,
			Name:       req.Name,
			OwnerID:    vibedauth.UserIDFromContext(ctx),
			Namespace:  namespace,
			Status:     api.StatusPending,
			Language:   req.Language,
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
		lang = appspec.DetectLanguage(req.Files)
	}
	artifact.Language = lang

	// 6. Select deployment target (language-aware)
	preferred := api.DeploymentTarget(req.Target)
	if preferred == "" {
		preferred = api.DeploymentTarget(o.cfg.Deployment.PreferredTarget)
	}

	// 6a. Instant Preview fast path: route runnable, zero-/pre-baked-dependency
	// apps to a warm runner pod — instant, no container build.
	if eligible, reason := o.fastPathEligible(preferred, lang, req.Files); eligible {
		o.logger.Info("using instant preview fast path (skipping build)",
			"name", req.Name, "language", lang)
		artifact.Target = api.TargetRunner
		artifact.Mode = api.ModePreview
		return o.deployRunner(ctx, artifact)
	} else if preferred == api.TargetRunner {
		// Caller explicitly asked for the fast path but the app isn't eligible.
		o.failArtifact(ctx, artifact, fmt.Sprintf("not eligible for instant preview: %s", reason))
		return nil, &api.ErrInvalidInput{Field: "target", Message: "not eligible for instant preview: " + reason}
	}

	target, err := o.detector.SelectTarget(preferred)
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("selecting target: %v", err))
		return nil, err
	}
	artifact.Target = target
	artifact.Mode = api.ModeBuilt

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
	        Namespace: artifact.Namespace,
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
	// Pin to the immutable manifest digest when the builder reported one.
	// Falls back to the tag for builders that haven't been updated yet.
	artifact.ImageRef = pinnedImageRef(buildResult)

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

	o.finalizeDeployment(ctx, artifact, deployResult, artifact.OwnerID)

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
	bgCtx := vibedauth.WithUserID(o.lifeCtx, ownerID)

	bgCtx, span := o.tracer.Start(bgCtx, "orchestrator.Update",
		trace.WithAttributes(attribute.String("artifact.id", req.ArtifactID)))
	traceID := span.SpanContext().TraceID().String()
	o.logger.Info("async update started", "artifact_id", req.ArtifactID, "trace_id", traceID)

	// Mark as building synchronously so the caller can see progress immediately.
	o.updateStatus(ctx, artifact, api.StatusBuilding)

	go func() {
		defer span.End()
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				o.logger.Error("async update panicked", "artifact_id", req.ArtifactID, "trace_id", traceID, "panic", r, "stack", stack)
				if a, _ := o.store.Get(o.lifeCtx, req.ArtifactID); a != nil {
					o.failArtifact(o.lifeCtx, a, fmt.Sprintf("panic during update: %v", r))
				}
				span.RecordError(fmt.Errorf("panic: %v", r))
				span.SetStatus(codes.Error, "panic")
			}
		}()
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
		lang = appspec.DetectLanguage(req.Files)
	}
	artifact.Language = lang

	// Runner fast path: re-inject new source into the existing warm runner,
	// skip the build entirely. The target is fixed at first deploy.
	if artifact.Target == api.TargetRunner {
		o.logger.Info("using instant preview fast path for update (skipping build)", "name", artifact.Name)
		return o.updateRunner(ctx, artifact)
	}

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
	        Namespace: artifact.Namespace,
	        Language:  lang,
	        Env:       artifact.EnvVars,
	        Publish:   builderPublishes && o.cfg.Registry.Enabled,	})

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
	artifact.ImageRef = pinnedImageRef(buildResult)

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

	o.finalizeDeployment(ctx, artifact, deployResult, artifact.OwnerID)

	return &DeployResult{
		ArtifactID: artifact.ID,
		Name:       artifact.Name,
		URL:        deployResult.URL,
		Target:     target,
		Status:     string(api.StatusRunning),
		ImageRef:   buildResult.ImageRef,
	}, nil
}

// Delete stops and removes a deployed artifact, after an ownership check.
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

	return o.deleteArtifact(ctx, artifact)
}

// ReapArtifact deletes an artifact on behalf of the garbage collector — no
// ownership check, since the GC is a system actor. Used to reap expired
// fast-path previews (which also recycles their pooled runner).
func (o *Orchestrator) ReapArtifact(ctx context.Context, artifactID string) error {
	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return err
	}
	return o.deleteArtifact(ctx, artifact)
}

// deleteArtifact tears down an artifact's backend resources, stored source, and
// store record. It performs NO ownership check — callers must authorize first.
func (o *Orchestrator) deleteArtifact(ctx context.Context, artifact *api.Artifact) error {
	artifactID := artifact.ID

	// Cleanup deployed resources. If artifact.Target is empty (e.g. the build
	// crashed before target selection) we don't know which backend owns the
	// resources — sweep every registered deployer best-effort. Otherwise just
	// the chosen one. NotFound is silent; other errors are logged but do not
	// block store deletion.
	if artifact.Target != "" {
		dep, err := o.factory.Get(artifact.Target)
		if err != nil {
			o.logger.Warn("failed to get deployer for delete", "id", artifactID, "target", artifact.Target, "error", err)
		} else if err := dep.Delete(ctx, artifact); err != nil {
			o.logger.Warn("failed to delete deployment", "id", artifactID, "error", err)
		}
	} else {
		o.logger.Info("artifact has no Target — sweeping all backends", "id", artifactID)
		for target, dep := range o.factory.All() {
			if err := dep.Delete(ctx, artifact); err != nil {
				o.logger.Debug("backend sweep delete returned error (likely NotFound)",
					"id", artifactID, "target", target, "error", err)
			}
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

func (o *Orchestrator) finalizeDeployment(ctx context.Context, artifact *api.Artifact, deployResult *deployer.DeployResult, userID string) {
	artifact.URL = deployResult.URL
	artifact.Status = api.StatusRunning
	artifact.Error = ""
	artifact.UpdatedAt = time.Now()

	newVersion := artifact.Version + 1
	if newVersion <= 1 {
		newVersion = 2 // pre-versioning artifacts jump from 0 to 2
	}
	if artifact.Version == 0 {
		newVersion = 1 // First time deploy
	}
	artifact.Version = newVersion
	artifact.VersionID = generateID()

	if err := o.store.Update(ctx, artifact); err != nil {
		o.logger.Warn("failed to persist deploy result",
			"artifact_id", artifact.ID,
			"error", err,
		)
	}
	o.publishStatusEvent(artifact)

	o.createVersionSnapshot(ctx, artifact)
}
func (o *Orchestrator) failArtifact(_ context.Context, artifact *api.Artifact, reason string) {
	failCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Enrich with cluster-side diagnostics where possible. Best-effort:
	// failures to fetch don't mask the original reason.
	if diag := o.collectDeployDiagnostics(failCtx, artifact); diag != "" {
		reason = reason + "\n" + diag
	}

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

// collectDeployDiagnostics builds a short human-readable summary of why a
// deploy might have failed: relevant K8s resource Conditions and the last
// few namespace Events tagged with the artifact's name. Returns an empty
// string when nothing useful could be fetched (e.g. resource never created).
func (o *Orchestrator) collectDeployDiagnostics(ctx context.Context, artifact *api.Artifact) string {
	if artifact == nil || artifact.Namespace == "" || artifact.Name == "" {
		return ""
	}
	var parts []string

	// Recent events tagged with the artifact name. Works across all targets
	// because vibeD names its objects after the artifact.
	if events, err := o.clientset.CoreV1().Events(artifact.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", artifact.Name),
		Limit:         10,
	}); err == nil && len(events.Items) > 0 {
		// Sort newest-first by lastTimestamp.
		sort.Slice(events.Items, func(i, j int) bool {
			return events.Items[i].LastTimestamp.After(events.Items[j].LastTimestamp.Time)
		})
		seen := 0
		var evLines []string
		for _, ev := range events.Items {
			if seen >= 5 {
				break
			}
			if ev.Type == "Normal" {
				continue // surface warnings / errors only
			}
			evLines = append(evLines, fmt.Sprintf("  [%s] %s: %s", ev.Type, ev.Reason, strings.TrimSpace(ev.Message)))
			seen++
		}
		if len(evLines) > 0 {
			parts = append(parts, "Recent events:\n"+strings.Join(evLines, "\n"))
		}
	}

	// Pod-level diagnostics: surface waiting reasons (ImagePullBackOff,
	// CrashLoopBackOff, RunContainerError, …). Limited to the first matching
	// pod; vibeD typically deploys 1 replica.
	podSelector := fmt.Sprintf("vibed.dev/artifact-id=%s", artifact.ID)
	if pods, err := o.clientset.CoreV1().Pods(artifact.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: podSelector,
		Limit:         5,
	}); err == nil {
		var podLines []string
		for _, pod := range pods.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
					podLines = append(podLines, fmt.Sprintf("  pod %s container %s: %s — %s",
						pod.Name, cs.Name, cs.State.Waiting.Reason, strings.TrimSpace(cs.State.Waiting.Message)))
				}
				if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" && cs.State.Terminated.Reason != "Completed" {
					podLines = append(podLines, fmt.Sprintf("  pod %s container %s terminated: %s — %s",
						pod.Name, cs.Name, cs.State.Terminated.Reason, strings.TrimSpace(cs.State.Terminated.Message)))
				}
			}
		}
		if len(podLines) > 0 {
			parts = append(parts, "Pod state:\n"+strings.Join(podLines, "\n"))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return "--- diagnostics ---\n" + strings.Join(parts, "\n")
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

// pinnedImageRef returns image@sha256:... when the builder captured a digest,
// or the original ImageRef tag otherwise. Pinning by digest avoids registry
// caching surprises and makes Knative revisions reproducibly distinct.
func pinnedImageRef(r *builder.BuildResult) string {
	if r == nil {
		return ""
	}
	if r.Digest == "" {
		return r.ImageRef
	}
	// Strip any trailing tag — `repo:tag@digest` is invalid.
	ref := r.ImageRef
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref + "@" + r.Digest
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

	cmLabels := map[string]string{
		"app.kubernetes.io/managed-by": "vibed",
		"vibed.dev/artifact-id":        artifact.ID,
	}
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		cmLabels["vibed.dev/trace-id"] = span.SpanContext().TraceID().String()
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: ns,
			Labels:    cmLabels,
		},
		Data: data,
	}

	// Idempotent apply: if a previous failed deploy left this ConfigMap behind
	// we Update it instead of failing. The label/data we want is fully derived
	// from the current request, so overwriting is safe.
	if _, err := o.clientset.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			o.failArtifact(ctx, artifact, fmt.Sprintf("creating static ConfigMap: %v", err))
			return nil, fmt.Errorf("creating static ConfigMap: %w", err)
		}
		if _, uerr := o.clientset.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); uerr != nil {
			o.failArtifact(ctx, artifact, fmt.Sprintf("updating existing static ConfigMap: %v", uerr))
			return nil, fmt.Errorf("updating existing static ConfigMap: %w", uerr)
		}
	}

	// Set artifact fields for static deploy.
	//
	// nginxinc/nginx-unprivileged is the official image built to run as a
	// non-root user (UID 101) with /tmp writable. Stock nginx:alpine binds
	// :80 as root and writes to /var/cache/nginx — both incompatible with
	// the runAsNonRoot:true podSecurityContext we set on every workload.
	artifact.ImageRef = "nginxinc/nginx-unprivileged:alpine"
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

	o.finalizeDeployment(ctx, artifact, deployResult, artifact.OwnerID)

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

// fastPathEligible reports whether a deploy should take the Instant Preview
// fast path. It requires: the fast path enabled, a RunnerDeployer registered,
// no other backend explicitly pinned by the caller, a runner configured for
// the language, and the dependency gate cleared. The returned string explains
// a negative result — surfaced when the caller explicitly requested "runner".
func (o *Orchestrator) fastPathEligible(preferred api.DeploymentTarget, lang string, files map[string]string) (bool, string) {
	if !o.cfg.FastPath.Enabled {
		return false, "fast path is not enabled"
	}
	if _, err := o.factory.Get(api.TargetRunner); err != nil {
		return false, "no runner deployer registered"
	}
	// Respect a backend the caller explicitly pinned (anything but auto/runner).
	if preferred != "" && preferred != api.TargetAuto && preferred != api.TargetRunner {
		return false, fmt.Sprintf("target %q was explicitly requested", preferred)
	}
	if _, ok := o.cfg.FastPath.Runners[lang]; !ok {
		return false, fmt.Sprintf("no runner configured for language %q", lang)
	}
	if ok, missing := prebaked.Eligible(lang, files); !ok {
		return false, "dependencies not pre-baked: " + strings.Join(missing, ", ")
	}
	return true, ""
}

// deployRunner deploys an artifact via the Instant Preview fast path: a warm
// pooled runner pod with the source injected, no container build.
func (o *Orchestrator) deployRunner(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	o.updateStatus(ctx, artifact, api.StatusDeploying)

	dep, err := o.factory.Get(api.TargetRunner)
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("no runner deployer: %v", err))
		return nil, err
	}

	deployCtx, deploySpan := o.tracer.Start(ctx, "deployer.Deploy",
		trace.WithAttributes(attribute.String("deployer.target", string(api.TargetRunner))))
	deployStart := time.Now()
	deployResult, err := dep.Deploy(deployCtx, artifact)
	deployDur := time.Since(deployStart).Seconds()

	if err != nil {
		deploySpan.RecordError(err)
		deploySpan.SetStatus(codes.Error, err.Error())
		deploySpan.End()
		o.metrics.DeploysTotal.WithLabelValues("failed", string(api.TargetRunner)).Inc()
		o.metrics.DeployDuration.WithLabelValues("failed", string(api.TargetRunner)).Observe(deployDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("runner deploy failed: %v", err))
		return nil, &api.ErrDeployFailed{Reason: err.Error()}
	}
	deploySpan.End()

	o.metrics.DeploysTotal.WithLabelValues("success", string(api.TargetRunner)).Inc()
	o.metrics.DeployDuration.WithLabelValues("success", string(api.TargetRunner)).Observe(deployDur)
	o.metrics.ArtifactsActive.WithLabelValues(string(api.TargetRunner)).Inc()

	o.finalizeDeployment(ctx, artifact, deployResult, artifact.OwnerID)

	o.logger.Info("artifact deployed via instant preview (no build)",
		"id", artifact.ID, "name", artifact.Name, "url", deployResult.URL, "version", 1)

	// fastPath.autoPromote: materialize the durable built artifact in the
	// background. The preview keeps serving until the swap; a promote failure
	// leaves it running.
	if o.cfg.FastPath.AutoPromote {
		go o.autoPromote(artifact.ID, artifact.OwnerID)
	}

	return &DeployResult{
		ArtifactID: artifact.ID,
		Name:       artifact.Name,
		URL:        deployResult.URL,
		Target:     string(api.TargetRunner),
		Status:     string(api.StatusRunning),
	}, nil
}

// updateRunner re-injects new source into the artifact's existing warm runner.
func (o *Orchestrator) updateRunner(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	o.updateStatus(ctx, artifact, api.StatusDeploying)

	dep, err := o.factory.Get(api.TargetRunner)
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("no runner deployer: %v", err))
		return nil, err
	}

	deployCtx, deploySpan := o.tracer.Start(ctx, "deployer.Update",
		trace.WithAttributes(attribute.String("deployer.target", string(api.TargetRunner))))
	deployStart := time.Now()
	deployResult, err := dep.Update(deployCtx, artifact)
	deployDur := time.Since(deployStart).Seconds()

	if err != nil {
		deploySpan.RecordError(err)
		deploySpan.SetStatus(codes.Error, err.Error())
		deploySpan.End()
		o.metrics.DeploysTotal.WithLabelValues("failed", string(api.TargetRunner)).Inc()
		o.metrics.DeployDuration.WithLabelValues("failed", string(api.TargetRunner)).Observe(deployDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("runner update failed: %v", err))
		return nil, &api.ErrDeployFailed{Reason: err.Error()}
	}
	deploySpan.End()

	o.metrics.DeploysTotal.WithLabelValues("success", string(api.TargetRunner)).Inc()
	o.metrics.DeployDuration.WithLabelValues("success", string(api.TargetRunner)).Observe(deployDur)

	o.finalizeDeployment(ctx, artifact, deployResult, artifact.OwnerID)

	o.logger.Info("artifact updated via instant preview (no build)",
		"id", artifact.ID, "name", artifact.Name, "url", deployResult.URL)

	return &DeployResult{
		ArtifactID: artifact.ID,
		Name:       artifact.Name,
		URL:        deployResult.URL,
		Target:     string(api.TargetRunner),
		Status:     string(api.StatusRunning),
	}, nil
}

// canPromote reports whether an artifact is in a state that can be promoted:
// a running fast-path preview backed by a pooled runner.
func canPromote(a *api.Artifact) error {
	if a.Mode != api.ModePreview || a.Target != api.TargetRunner {
		return &api.ErrInvalidInput{Field: "artifact_id", Message: "only a fast-path preview can be promoted"}
	}
	return nil
}

// AsyncPromote validates ownership synchronously, then runs the promote
// (build → deploy → swap → release runner) in a background goroutine. Returns
// immediately with status "building"; poll get_artifact_status.
func (o *Orchestrator) AsyncPromote(ctx context.Context, artifactID string) (*DeployResult, error) {
	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if err := o.checkWriteOwnership(ctx, artifact); err != nil {
		return nil, err
	}
	if err := canPromote(artifact); err != nil {
		return nil, err
	}

	ownerID := vibedauth.UserIDFromContext(ctx)
	bgCtx := vibedauth.WithUserID(o.lifeCtx, ownerID)
	bgCtx, span := o.tracer.Start(bgCtx, "orchestrator.Promote",
		trace.WithAttributes(attribute.String("artifact.id", artifactID)))
	traceID := span.SpanContext().TraceID().String()
	o.logger.Info("async promote started", "artifact_id", artifactID, "trace_id", traceID)

	// Mark building synchronously so the caller sees progress immediately.
	o.updateStatus(ctx, artifact, api.StatusBuilding)

	go func() {
		defer span.End()
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				o.logger.Error("async promote panicked", "artifact_id", artifactID, "trace_id", traceID, "panic", r, "stack", stack)
				if a, _ := o.store.Get(o.lifeCtx, artifactID); a != nil {
					o.failArtifact(o.lifeCtx, a, fmt.Sprintf("panic during promote: %v", r))
				}
				span.RecordError(fmt.Errorf("panic: %v", r))
				span.SetStatus(codes.Error, "panic")
			}
		}()
		result, err := o.doPromote(bgCtx, artifactID)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			o.logger.Error("async promote failed", "artifact_id", artifactID, "trace_id", traceID, "error", err)
		} else {
			span.SetStatus(codes.Ok, "")
			o.logger.Info("async promote completed", "artifact_id", artifactID, "trace_id", traceID, "url", result.URL)
		}
	}()

	return &DeployResult{
		ArtifactID: artifactID,
		Name:       artifact.Name,
		Status:     string(api.StatusBuilding),
	}, nil
}

// autoPromote runs doPromote in the background for fastPath.autoPromote. It
// derives from the lifecycle context and recovers panics — a promote failure
// must never take down the server, and the preview keeps running on failure.
func (o *Orchestrator) autoPromote(artifactID, ownerID string) {
	defer func() {
		if r := recover(); r != nil {
			o.logger.Error("auto-promote panicked", "artifact_id", artifactID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	ctx := vibedauth.WithUserID(o.lifeCtx, ownerID)
	ctx, span := o.tracer.Start(ctx, "orchestrator.AutoPromote",
		trace.WithAttributes(attribute.String("artifact.id", artifactID)))
	defer span.End()
	if _, err := o.doPromote(ctx, artifactID); err != nil {
		o.logger.Warn("auto-promote failed; preview left running", "artifact_id", artifactID, "error", err)
	}
}

// doPromote upgrades a running fast-path preview into a durable built artifact:
// it runs the real container build, deploys the digest-pinned image to a real
// backend, swaps the live deployment, and releases the pooled runner. The
// artifact ID is stable across the promote; the URL changes (callers re-fetch
// it via get_artifact_status). On any failure the preview is restored and left
// running — a failed promote never takes the preview down.
func (o *Orchestrator) doPromote(ctx context.Context, artifactID string) (*DeployResult, error) {
	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if err := canPromote(artifact); err != nil {
		return nil, err
	}

	// The runner deployer still owns the live preview pod; capture it now so we
	// can release the runner only once the built deployment is confirmed up.
	runnerDep, err := o.factory.Get(api.TargetRunner)
	if err != nil {
		return nil, fmt.Errorf("runner deployer unavailable: %w", err)
	}

	// Snapshot the preview state so a failed promote can restore it verbatim.
	prevTarget, prevMode := artifact.Target, artifact.Mode
	prevImageRef, prevURL, prevStatus := artifact.ImageRef, artifact.URL, artifact.Status
	restorePreview := func() {
		artifact.Target, artifact.Mode = prevTarget, prevMode
		artifact.ImageRef, artifact.URL, artifact.Status = prevImageRef, prevURL, prevStatus
		artifact.Error = ""
		artifact.UpdatedAt = time.Now()
		if uerr := o.store.Update(o.lifeCtx, artifact); uerr != nil {
			o.logger.Warn("promote: failed to restore preview state", "artifact_id", artifactID, "error", uerr)
		}
		o.publishStatusEvent(artifact)
	}

	o.updateStatus(ctx, artifact, api.StatusBuilding)

	// 1. Build the real image from the already-stored source.
	sourceDir, err := o.storage.GetSourcePath(ctx, artifact.ID)
	if err != nil {
		restorePreview()
		return nil, fmt.Errorf("locating source: %w", err)
	}
	imageName := fmt.Sprintf("%s/%s:latest", o.imageBase, artifact.Name)

	o.metrics.BuildsInFlight.Inc()
	buildStart := time.Now()
	builderPublishes := o.builder.PublishesInternally()
	buildCtx, buildSpan := o.tracer.Start(ctx, "builder.Build",
		trace.WithAttributes(
			attribute.String("builder.image", imageName),
			attribute.String("builder.language", artifact.Language),
		))
	buildResult, err := o.builder.Build(buildCtx, builder.BuildRequest{
		SourceDir: sourceDir,
		ImageName: imageName,
		Namespace: artifact.Namespace,
		Language:  artifact.Language,
		Env:       artifact.EnvVars,
		Publish:   builderPublishes && o.cfg.Registry.Enabled,
	})
	o.metrics.BuildsInFlight.Dec()
	buildDur := time.Since(buildStart).Seconds()
	if err != nil {
		buildSpan.RecordError(err)
		buildSpan.SetStatus(codes.Error, err.Error())
		buildSpan.End()
		o.metrics.BuildsTotal.WithLabelValues("failed", artifact.Language).Inc()
		o.metrics.BuildDuration.WithLabelValues("failed", artifact.Language).Observe(buildDur)
		restorePreview()
		return nil, &api.ErrBuildFailed{Reason: err.Error()}
	}
	buildSpan.End()
	o.metrics.BuildsTotal.WithLabelValues("success", artifact.Language).Inc()
	o.metrics.BuildDuration.WithLabelValues("success", artifact.Language).Observe(buildDur)

	// 2. Select a durable backend and deploy the built image.
	target, err := o.detector.SelectTarget(api.DeploymentTarget(o.cfg.Deployment.PreferredTarget))
	if err != nil {
		restorePreview()
		return nil, fmt.Errorf("selecting target: %w", err)
	}
	dep, err := o.factory.Get(target)
	if err != nil {
		restorePreview()
		return nil, fmt.Errorf("no deployer for target %q: %w", target, err)
	}

	// Mutate the artifact for the built deployment. If the deploy fails we tear
	// down whatever it created and restore the preview state.
	artifact.ImageRef = pinnedImageRef(buildResult)
	artifact.Target = target
	o.updateStatus(ctx, artifact, api.StatusDeploying)

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
		_ = dep.Delete(context.WithoutCancel(ctx), artifact) // best-effort cleanup
		restorePreview()
		return nil, &api.ErrDeployFailed{Reason: err.Error()}
	}
	deploySpan.End()
	o.metrics.DeploysTotal.WithLabelValues("success", string(target)).Inc()
	o.metrics.DeployDuration.WithLabelValues("success", string(target)).Observe(deployDur)
	o.metrics.ArtifactsActive.WithLabelValues(string(target)).Inc()

	// 3. Swap confirmed — release the pooled runner (the RunnerDeployer keys by
	// artifact ID, so this still finds it despite Target having changed) and
	// flip the artifact to a durable built mode.
	if delErr := runnerDep.Delete(context.WithoutCancel(ctx), artifact); delErr != nil {
		o.logger.Warn("promote: failed to release pooled runner", "artifact_id", artifactID, "error", delErr)
	}
	o.metrics.ArtifactsActive.WithLabelValues(string(api.TargetRunner)).Dec()

	artifact.Mode = api.ModeBuilt
	o.finalizeDeployment(ctx, artifact, deployResult, artifact.OwnerID)

	o.logger.Info("artifact promoted from preview to built",
		"id", artifact.ID, "name", artifact.Name, "target", target, "url", deployResult.URL)

	return &DeployResult{
		ArtifactID: artifact.ID,
		Name:       artifact.Name,
		URL:        deployResult.URL,
		Target:     string(target),
		Status:     string(api.StatusRunning),
		ImageRef:   buildResult.ImageRef,
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

	newVersion := artifact.Version + 1
	if newVersion <= 1 {
		newVersion = 2
	}
	newCmName := fmt.Sprintf("%s-v%d-static", artifact.ID, newVersion)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newCmName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "vibed",
				"vibed.dev/artifact-id":        artifact.ID,
			},
		},
		Data: data,
	}

	// Create new ConfigMap first
	_, err := o.clientset.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("updating static ConfigMap: %v", err))
		return nil, fmt.Errorf("updating static ConfigMap: %w", err)
	}

	// Delete old ConfigMap
	oldCmName := artifact.StaticFiles
	if oldCmName != "" {
		_ = o.clientset.CoreV1().ConfigMaps(ns).Delete(ctx, oldCmName, metav1.DeleteOptions{})
	}

	artifact.ImageRef = "nginx:alpine"
	artifact.StaticFiles = newCmName
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

	o.finalizeDeployment(ctx, artifact, deployResult, artifact.OwnerID)

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
	o.finalizeDeployment(ctx, artifact, deployResult, artifact.OwnerID)

	o.logger.Info("artifact rolled back",
		"id", artifactID,
		"from_version", artifact.Version-1,
		"to_snapshot", targetVersion,
		"new_version", artifact.Version,
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
