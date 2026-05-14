package builder

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vibed-project/vibeD/internal/config"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"go.opentelemetry.io/otel/trace"
)

// BuildahBuilder builds container images by creating Kubernetes Jobs
// that run Buildah. This avoids requiring a Docker/Podman socket.
type BuildahBuilder struct {
	clientset    kubernetes.Interface
	namespace    string
	buildahImage string
	insecure     bool
	pvcName      string
	storagePath  string // PVC mount point base, e.g. "/data/vibed"
	timeout      time.Duration
	logger       *slog.Logger
}

// NewBuildahBuilder creates a new BuildahBuilder.
func NewBuildahBuilder(
	clientset kubernetes.Interface,
	cfg config.BuildahConfig,
	registry config.RegistryConfig,
	namespace string,
	pvcName string,
	storagePath string,
	logger *slog.Logger,
) *BuildahBuilder {
	buildahImage := cfg.Image
	if buildahImage == "" {
		buildahImage = "quay.io/buildah/stable:latest"
	}

	timeout := 10 * time.Minute
	if cfg.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Timeout); err == nil {
			timeout = d
		}
	}

	return &BuildahBuilder{
		clientset:    clientset,
		namespace:    namespace,
		buildahImage: buildahImage,
		insecure:     cfg.Insecure,
		pvcName:      pvcName,
		storagePath:  storagePath,
		timeout:      timeout,
		logger:       logger,
	}
}

func (b *BuildahBuilder) Build(ctx context.Context, req BuildRequest) (*BuildResult, error) {
	b.logger.Info("building container image via Buildah Job",
		"source", req.SourceDir,
		"image", req.ImageName,
		"language", req.Language,
	)

	// 1. Scan source directory for language auto-detection and write Dockerfile
	files := make(map[string]string)
	entries, err := os.ReadDir(req.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("reading source directory %q: %w", req.SourceDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			files[e.Name()] = ""
		}
	}
	
	dockerfilePath := filepath.Join(req.SourceDir, "Dockerfile")
	
	// Check if a Dockerfile already exists (case-insensitive check)
	hasCustomDockerfile := false
	for name := range files {
		if strings.EqualFold(name, "Dockerfile") {
			hasCustomDockerfile = true
			b.logger.Info("using provided custom Dockerfile", "file", name)
			break
		}
	}

	if !hasCustomDockerfile {
		dockerfile := GenerateDockerfile(req.Language, files)
		if err := os.WriteFile(dockerfilePath, []byte(dockerfile), 0644); err != nil {
			return nil, fmt.Errorf("writing generated Dockerfile: %w", err)
		}
	}

	// 2. Compute sub-path relative to PVC mount
	subPath := strings.TrimPrefix(req.SourceDir, b.storagePath+"/")

	// 3. Create a unique job name (use artifact ID from parent dir, not "src")
	shortID := filepath.Base(filepath.Dir(req.SourceDir))
	if len(shortID) > 16 {
		shortID = shortID[:16]
	}
	jobName := fmt.Sprintf("vibed-build-%s", shortID)

	// 4. Validate image name to prevent shell injection
	if err := validateImageName(req.ImageName); err != nil {
		return nil, fmt.Errorf("invalid image name: %w", err)
	}

	// 5. Build the Buildah command
	tlsVerify := "true"
	if b.insecure {
		tlsVerify = "false"
	}
	// After push, write the manifest digest to a marker line vibeD parses
	// from the Job logs. We pin the deployed image to image@sha256 so
	// redeploys can't accidentally pull a different blob from registry cache.
	buildScript := `set -e
buildah bud --storage-driver=vfs --isolation=chroot -t "$1" /workspace
buildah push --storage-driver=vfs --tls-verify="$2" --digestfile /tmp/vibed-digest "$1" "docker://$1"
echo "VIBED_DIGEST=$(cat /tmp/vibed-digest)"`

	// 5. Create K8s Job
	ns := req.Namespace
	if ns == "" {
	        ns = b.namespace // fallback to global
	}

	jobLabels := map[string]string{
		"app.kubernetes.io/managed-by": "vibed",
		"vibed.dev/component":          "build",
	}
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		jobLabels["vibed.dev/trace-id"] = span.SpanContext().TraceID().String()
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ns,
			Labels:    jobLabels,
		},
		Spec: batchv1.JobSpec{
			// Tolerate transient pull/registry/eviction failures with up to
			// 2 retries, but cap the entire build via ActiveDeadlineSeconds
			// so a stuck pod doesn't block the artifact forever.
			BackoffLimit:            ptr.To(int32(2)),
			ActiveDeadlineSeconds:   ptr.To(int64(b.timeout.Seconds())),
			// Keep the Job around for 10 minutes after completion so
			// fetchJobLogs has a chance to grab logs even after a vibeD
			// restart. Stale finished Jobs get GC'd by the periodic GC sweep.
			TTLSecondsAfterFinished: ptr.To(int32(600)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					// Pod-level securityContext: tighten what we can while
					// still allowing rootless Buildah to function. Buildah's
					// in-pod uid-mapping needs newuidmap (setuid binary), so
					// AllowPrivilegeEscalation cannot be set to false on the
					// container — keep this Job's namespace at PSA `baseline`
					// or higher.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(true),
						RunAsUser:    ptr.To(int64(1000)),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "buildah",
							Image:   b.buildahImage,
							Command: []string{"sh", "-c"},
							Args:    []string{buildScript, "build-script", req.ImageName, tlsVerify},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "source",
									MountPath: "/workspace",
									SubPath:   subPath,
									ReadOnly:  true,
								},
							},
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
									Add:  []corev1.Capability{"SETUID", "SETGID"},
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "source",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: b.pvcName,
								},
							},
						},
					},
				},
			},
		},
	}
b.logger.Info("creating build Job", "job", jobName, "namespace", ns)
_, err = b.clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
if k8serrors.IsAlreadyExists(err) {
        b.logger.Warn("stale build Job exists, deleting and retrying", "job", jobName)
        b.cleanup(ctx, ns, jobName)

        // Wait for deletion with exponential backoff rather than a fixed 2s sleep
        interval := 100 * time.Millisecond
        for i := 0; i < 15; i++ {
                _, checkErr := b.clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
                if k8serrors.IsNotFound(checkErr) {
                        break
                }
                time.Sleep(interval)
                interval *= 2
                if interval > 2*time.Second {
                        interval = 2 * time.Second
                }
        }

        _, err = b.clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
}
	if err != nil {
		return nil, fmt.Errorf("creating build Job: %w", err)
	}

	// 6. Wait for Job completion
	err = b.waitForJob(ctx, ns, jobName)
	if err != nil {
		// Fetch logs for debugging
		logs := b.fetchJobLogs(ctx, ns, jobName)
		b.cleanup(ctx, ns, jobName)
		return nil, fmt.Errorf("build failed: %w\nBuild logs:\n%s", err, logs)
		}

	// Parse pushed-image digest from Job logs before cleanup. Best-effort:
	// digest pinning is an optimization, not a hard requirement.
	digest := parseDigestFromLogs(b.fetchJobLogs(ctx, ns, jobName))
	b.logger.Info("build completed", "image", req.ImageName, "digest", digest)
	b.cleanup(ctx, ns, jobName)

	return &BuildResult{
		ImageRef: req.ImageName,
		Digest:   digest,
	}, nil
}

// parseDigestFromLogs extracts the value of the marker line we emit after
// a successful push: "VIBED_DIGEST=sha256:...". Returns "" if absent.
func parseDigestFromLogs(logs string) string {
	const marker = "VIBED_DIGEST="
	for _, line := range strings.Split(logs, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, marker) {
			d := strings.TrimPrefix(line, marker)
			if strings.HasPrefix(d, "sha256:") {
				return d
			}
		}
	}
	return ""
}

func (b *BuildahBuilder) waitForJob(ctx context.Context, namespace, jobName string) error {
	// Honor the parent context (orchestrator lifecycle) AND cap with timeout.
	// This way SIGTERM cancels in-flight builds instead of leaking them, and
	// runaway builds still hit the b.timeout ceiling.
	waitCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	interval := 2 * time.Second

	for {
		// Check context before doing API call
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("build timed out after %v", b.timeout)
		default:
		}

		job, err := b.clientset.BatchV1().Jobs(namespace).Get(waitCtx, jobName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("checking Job status: %w", err)
		}

		if job.Status.Succeeded > 0 {
			return nil
		}
		if job.Status.Failed > 0 {
			return fmt.Errorf("build Job failed")
		}

		// Wait with exponential backoff
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("build timed out after %v", b.timeout)
		case <-time.After(interval):
			if interval < 15*time.Second {
				interval = time.Duration(float64(interval) * 1.5)
			}
		}
	}
}

func (b *BuildahBuilder) fetchJobLogs(_ context.Context, namespace, jobName string) string {
        logCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
        defer cancel()

        pods, err := b.clientset.CoreV1().Pods(namespace).List(logCtx, metav1.ListOptions{		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	if err != nil || len(pods.Items) == 0 {
		return "(no build logs available)"
	}

	tailLines := int64(50)
	req := b.clientset.CoreV1().Pods(namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{		TailLines: &tailLines,
	})
	stream, err := req.Stream(logCtx)
	if err != nil {
		return fmt.Sprintf("(failed to fetch logs: %v)", err)
	}
	defer stream.Close()

	var lines []string
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return strings.Join(lines, "\n")
}

func (b *BuildahBuilder) cleanup(_ context.Context, namespace, jobName string) {
        cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        propagation := metav1.DeletePropagationBackground
        err := b.clientset.BatchV1().Jobs(namespace).Delete(cleanCtx, jobName, metav1.DeleteOptions{		PropagationPolicy: &propagation,
	})
	if err != nil && !k8serrors.IsNotFound(err) {
		b.logger.Warn("failed to cleanup build Job", "job", jobName, "error", err)
	}
}

// PublishesInternally returns true: BuildahBuilder pushes to the registry inside the K8s Job.
func (b *BuildahBuilder) PublishesInternally() bool { return true }
