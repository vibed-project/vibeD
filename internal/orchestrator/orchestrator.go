package orchestrator

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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
	builder     builder.Builder
	wasmBuilder builder.Builder // optional: builds wasm components for wasmCloud target
	factory     *deployer.Factory
	storage     storage.Storage
	store       store.ArtifactStore
	metrics     *metrics.Metrics
	clientset   kubernetes.Interface
	events      *events.EventBus
	imageBase   string
	logger      *slog.Logger
}

// NewOrchestrator creates a new Orchestrator with all subsystems wired.
func NewOrchestrator(
	cfg *config.Config,
	detector *environment.Detector,
	bldr builder.Builder,
	wasmBldr builder.Builder,
	factory *deployer.Factory,
	stg storage.Storage,
	st store.ArtifactStore,
	m *metrics.Metrics,
	clientset kubernetes.Interface,
	bus *events.EventBus,
	logger *slog.Logger,
) *Orchestrator {
	imageBase := "vibed-artifacts"
	if cfg.Registry.Enabled && cfg.Registry.URL != "" {
		imageBase = cfg.Registry.URL
	}

	return &Orchestrator{
		cfg:         cfg,
		detector:    detector,
		builder:     bldr,
		wasmBuilder: wasmBldr,
		factory:     factory,
		storage:     stg,
		store:       st,
		metrics:     m,
		clientset:   clientset,
		events:      bus,
		imageBase:   imageBase,
		logger:      logger,
	}
}

// Deploy handles the full deployment flow: validate → store → build → deploy.
func (o *Orchestrator) Deploy(ctx context.Context, req DeployRequest) (*DeployResult, error) {
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

	// 5. Detect language early (needed for target selection)
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

	// For compiled wasm-capable languages (Go, Rust), prefer wasmCloud when available
	if (preferred == "" || preferred == "auto") && isWasmCapable(lang) && o.wasmBuilder != nil {
		result := o.detector.Detect()
		if result.WasmCloud {
			preferred = api.TargetWasmCloud
			o.logger.Info("auto-selected wasmCloud for compiled language",
				"name", req.Name, "language", lang)
		}
	}

	target, err := o.detector.SelectTarget(preferred)
	if err != nil {
		o.failArtifact(ctx, artifact, fmt.Sprintf("selecting target: %v", err))
		return nil, err
	}
	artifact.Target = target

	// Static shortcut: skip build, use ConfigMap + nginx directly.
	// wasmCloud cannot serve static files, so fall back to another target.
	if lang == "static" && isSmallStatic(req.Files) {
		if target == api.TargetWasmCloud {
			o.logger.Info("wasmCloud cannot serve static files, falling back",
				"name", req.Name)
			fallback, err := o.detector.SelectTarget("auto")
			if err != nil || fallback == api.TargetWasmCloud {
				fallback = api.TargetKubernetes
			}
			target = fallback
			artifact.Target = target
		}
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
	if target == api.TargetWasmCloud && o.wasmBuilder != nil {
		activeBuilder = o.wasmBuilder
	}

	buildResult, err := activeBuilder.Build(ctx, builder.BuildRequest{
		SourceDir: storageRef.LocalPath,
		ImageName: imageName,
		Language:  lang,
		Env:       req.EnvVars,
		Publish:   o.cfg.Registry.Enabled,
	})

	buildDur := time.Since(buildStart).Seconds()

	if err != nil {
		o.metrics.BuildsTotal.WithLabelValues("failed", lang).Inc()
		o.metrics.BuildDuration.WithLabelValues("failed", lang).Observe(buildDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("build failed: %v", err))
		return nil, &api.ErrBuildFailed{Reason: err.Error()}
	}

	o.metrics.BuildsTotal.WithLabelValues("success", lang).Inc()
	o.metrics.BuildDuration.WithLabelValues("success", lang).Observe(buildDur)
	artifact.ImageRef = buildResult.ImageRef

	// 8. Deploy
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

// Update rebuilds and redeploys an existing artifact.
func (o *Orchestrator) Update(ctx context.Context, req UpdateRequest) (*DeployResult, error) {
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
	if artifact.Target == api.TargetWasmCloud && o.wasmBuilder != nil {
		activeBuilder = o.wasmBuilder
	}

	buildResult, err := activeBuilder.Build(ctx, builder.BuildRequest{
		SourceDir: storageRef.LocalPath,
		ImageName: imageName,
		Language:  lang,
		Env:       artifact.EnvVars,
		Publish:   o.cfg.Registry.Enabled,
	})

	buildDur := time.Since(buildStart).Seconds()

	if err != nil {
		o.metrics.BuildsTotal.WithLabelValues("failed", lang).Inc()
		o.metrics.BuildDuration.WithLabelValues("failed", lang).Observe(buildDur)
		o.failArtifact(ctx, artifact, fmt.Sprintf("build failed: %v", err))
		return nil, &api.ErrBuildFailed{Reason: err.Error()}
	}

	o.metrics.BuildsTotal.WithLabelValues("success", lang).Inc()
	o.metrics.BuildDuration.WithLabelValues("success", lang).Observe(buildDur)
	artifact.ImageRef = buildResult.ImageRef

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

// List returns all artifacts matching the filter, scoped to the authenticated user.
// Admins see all artifacts. Regular users see owned + shared artifacts.
// When auth is disabled (no user in context), all artifacts are returned.
func (o *Orchestrator) List(ctx context.Context, statusFilter string) ([]api.ArtifactSummary, error) {
	ownerID := vibedauth.UserIDFromContext(ctx)
	adminView := vibedauth.IsAdmin(ctx)
	return o.store.List(ctx, statusFilter, ownerID, adminView)
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

// isWasmCapable returns true for compiled languages that can target WebAssembly.
func isWasmCapable(lang string) bool {
	switch lang {
	case "go", "rust":
		return true
	default:
		return false
	}
}

func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
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
	artifact, err := o.store.Get(ctx, artifactID)
	if err != nil {
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
