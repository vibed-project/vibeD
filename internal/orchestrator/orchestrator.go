package orchestrator

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	vibedauth "github.com/maxkorbacher/vibed/internal/auth"
	"github.com/maxkorbacher/vibed/internal/builder"
	"github.com/maxkorbacher/vibed/internal/config"
	"github.com/maxkorbacher/vibed/internal/deployer"
	"github.com/maxkorbacher/vibed/internal/environment"
	"github.com/maxkorbacher/vibed/internal/metrics"
	"github.com/maxkorbacher/vibed/internal/storage"
	"github.com/maxkorbacher/vibed/internal/store"
	"github.com/maxkorbacher/vibed/pkg/api"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var dnsNameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// DeployRequest is the input for deploying a new artifact.
type DeployRequest struct {
	Name     string
	Files    map[string]string
	Language string
	Target   string
	EnvVars  map[string]string
	Port     int
}

// UpdateRequest is the input for updating an existing artifact.
type UpdateRequest struct {
	ArtifactID string
	Files      map[string]string
	EnvVars    map[string]string
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
	cfg       *config.Config
	detector  *environment.Detector
	builder   builder.Builder
	factory   *deployer.Factory
	storage   storage.Storage
	store     store.ArtifactStore
	metrics   *metrics.Metrics
	clientset kubernetes.Interface
	imageBase string
	logger    *slog.Logger
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
	logger *slog.Logger,
) *Orchestrator {
	imageBase := "vibed-artifacts"
	if cfg.Registry.Enabled && cfg.Registry.URL != "" {
		imageBase = cfg.Registry.URL
	}

	return &Orchestrator{
		cfg:       cfg,
		detector:  detector,
		builder:   bldr,
		factory:   factory,
		storage:   stg,
		store:     st,
		metrics:   m,
		clientset: clientset,
		imageBase: imageBase,
		logger:    logger,
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

	// 1c. Check for duplicate name
	if existing, _ := o.store.GetByName(ctx, req.Name); existing != nil {
		return nil, &api.ErrAlreadyExists{Name: req.Name}
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
		EnvVars:   req.EnvVars,
		Port:      req.Port,
		CreatedAt: now,
		UpdatedAt: now,
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

	// 5. Select deployment target
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

	// 6. Detect language and check for static shortcut
	lang := req.Language
	if lang == "" || lang == "auto" {
		lang = builder.DetectLanguage(req.Files)
	}
	artifact.Language = lang

	// Static shortcut: skip build, use ConfigMap + nginx directly
	if lang == "static" && isSmallStatic(req.Files) {
		o.logger.Info("using static ConfigMap deploy (skipping build)",
			"name", req.Name, "files", len(req.Files))
		return o.deployStatic(ctx, artifact, req.Files, target)
	}

	// 7. Build container image (non-static path)
	imageName := fmt.Sprintf("%s/%s:latest", o.imageBase, req.Name)

	o.metrics.BuildsInFlight.Inc()
	defer o.metrics.BuildsInFlight.Dec()
	buildStart := time.Now()

	buildResult, err := o.builder.Build(ctx, builder.BuildRequest{
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

	// 9. Update artifact with URL and running status
	artifact.URL = deployResult.URL
	artifact.Status = api.StatusRunning
	artifact.UpdatedAt = time.Now()
	_ = o.store.Update(ctx, artifact)

	o.logger.Info("artifact deployed successfully",
		"id", artifactID,
		"name", req.Name,
		"target", target,
		"url", deployResult.URL,
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

	if err := o.checkOwnership(ctx, artifact); err != nil {
		return nil, err
	}

	// Validate file paths (prevent path traversal)
	for path := range req.Files {
		if err := validateFilePath(path); err != nil {
			return nil, err
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

	buildResult, err := o.builder.Build(ctx, builder.BuildRequest{
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

	artifact.URL = deployResult.URL
	artifact.Status = api.StatusRunning
	artifact.UpdatedAt = time.Now()
	_ = o.store.Update(ctx, artifact)

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

	if err := o.checkOwnership(ctx, artifact); err != nil {
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
// When auth is disabled (no user in context), all artifacts are returned.
func (o *Orchestrator) List(ctx context.Context, statusFilter string) ([]api.ArtifactSummary, error) {
	ownerID := vibedauth.UserIDFromContext(ctx)
	return o.store.List(ctx, statusFilter, ownerID)
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

// checkOwnership verifies that the current user owns the artifact.
// Returns ErrNotFound (not Forbidden) to avoid leaking artifact existence to non-owners.
// When auth is disabled (ownerID is empty), all ownership checks pass.
func (o *Orchestrator) checkOwnership(ctx context.Context, artifact *api.Artifact) error {
	ownerID := vibedauth.UserIDFromContext(ctx)
	if ownerID == "" {
		return nil // Auth disabled — no ownership enforcement
	}
	if artifact.OwnerID != "" && artifact.OwnerID != ownerID {
		return &api.ErrNotFound{ArtifactID: artifact.ID}
	}
	return nil
}

func (o *Orchestrator) updateStatus(ctx context.Context, artifact *api.Artifact, status api.ArtifactStatus) {
	artifact.Status = status
	artifact.UpdatedAt = time.Now()
	_ = o.store.Update(ctx, artifact)
}

func (o *Orchestrator) failArtifact(ctx context.Context, artifact *api.Artifact, reason string) {
	artifact.Status = api.StatusFailed
	artifact.Error = reason
	artifact.UpdatedAt = time.Now()
	_ = o.store.Update(ctx, artifact)
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
	artifact.UpdatedAt = time.Now()
	_ = o.store.Update(ctx, artifact)

	o.logger.Info("static artifact deployed (no build)",
		"id", artifact.ID, "name", artifact.Name,
		"target", target, "url", deployResult.URL)

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

	artifact.URL = deployResult.URL
	artifact.Status = api.StatusRunning
	artifact.UpdatedAt = time.Now()
	_ = o.store.Update(ctx, artifact)

	return &DeployResult{
		ArtifactID: artifact.ID,
		Name:       artifact.Name,
		URL:        deployResult.URL,
		Target:     target,
		Status:     string(api.StatusRunning),
		ImageRef:   "nginx:alpine",
	}, nil
}
