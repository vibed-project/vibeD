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

	"github.com/maxkorbacher/vibed/internal/config"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
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
	dockerfile := GenerateDockerfile(req.Language, files)
	dockerfilePath := filepath.Join(req.SourceDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(dockerfile), 0644); err != nil {
		return nil, fmt.Errorf("writing Dockerfile: %w", err)
	}

	// 2. Compute sub-path relative to PVC mount
	subPath := strings.TrimPrefix(req.SourceDir, b.storagePath+"/")

	// 3. Create a unique job name
	shortID := filepath.Base(req.SourceDir)
	if len(shortID) > 16 {
		shortID = shortID[:16]
	}
	jobName := fmt.Sprintf("vibed-build-%s", shortID)

	// 4. Build the Buildah command
	tlsVerify := "true"
	if b.insecure {
		tlsVerify = "false"
	}
	buildCmd := fmt.Sprintf(
		"buildah bud --storage-driver=vfs --isolation=chroot -t %s /workspace && "+
			"buildah push --storage-driver=vfs --tls-verify=%s %s docker://%s",
		req.ImageName, tlsVerify, req.ImageName, req.ImageName,
	)

	// 5. Create K8s Job
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: b.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "vibed",
				"vibed.dev/component":          "build",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(0)),
			TTLSecondsAfterFinished: ptr.To(int32(120)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "buildah",
							Image:   b.buildahImage,
							Command: []string{"sh", "-c"},
							Args:    []string{buildCmd},
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
									Add: []corev1.Capability{"SETUID", "SETGID"},
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

	b.logger.Info("creating build Job", "job", jobName, "namespace", b.namespace)
	_, err = b.clientset.BatchV1().Jobs(b.namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating build Job: %w", err)
	}

	// 6. Wait for Job completion
	err = b.waitForJob(ctx, jobName)
	if err != nil {
		// Fetch logs for debugging
		logs := b.fetchJobLogs(ctx, jobName)
		b.cleanup(ctx, jobName)
		return nil, fmt.Errorf("build failed: %w\nBuild logs:\n%s", err, logs)
	}

	b.logger.Info("build completed", "image", req.ImageName)
	b.cleanup(ctx, jobName)

	return &BuildResult{
		ImageRef: req.ImageName,
	}, nil
}

func (b *BuildahBuilder) waitForJob(ctx context.Context, jobName string) error {
	deadline := time.Now().Add(b.timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("build timed out after %v", b.timeout)
			}

			job, err := b.clientset.BatchV1().Jobs(b.namespace).Get(ctx, jobName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("checking Job status: %w", err)
			}

			if job.Status.Succeeded > 0 {
				return nil
			}
			if job.Status.Failed > 0 {
				return fmt.Errorf("build Job failed")
			}
		}
	}
}

func (b *BuildahBuilder) fetchJobLogs(ctx context.Context, jobName string) string {
	pods, err := b.clientset.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	if err != nil || len(pods.Items) == 0 {
		return "(no build logs available)"
	}

	tailLines := int64(50)
	req := b.clientset.CoreV1().Pods(b.namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{
		TailLines: &tailLines,
	})
	stream, err := req.Stream(ctx)
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

func (b *BuildahBuilder) cleanup(ctx context.Context, jobName string) {
	propagation := metav1.DeletePropagationBackground
	err := b.clientset.BatchV1().Jobs(b.namespace).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil {
		b.logger.Warn("failed to cleanup build Job", "job", jobName, "error", err)
	}
}
