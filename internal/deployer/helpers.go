package deployer

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/vibed-project/vibeD/pkg/api"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"go.opentelemetry.io/otel/trace"
)

// TraceIDLabel is the K8s label key carrying the OTel trace ID for the
// deploy that created the resource. Lets operators correlate vibeD logs
// with cluster-side resources via grep/kubectl.
const TraceIDLabel = "vibed.dev/trace-id"

// VibedLabels returns the standard vibeD-managed labels for an artifact's
// resources, including the OTel trace ID derived from ctx (when present).
// Use as the base label map for every K8s object vibeD creates.
func VibedLabels(ctx context.Context, artifact *api.Artifact) map[string]string {
	out := map[string]string{
		"app.kubernetes.io/managed-by": "vibed",
		"vibed.dev/artifact-id":        artifact.ID,
	}
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		out[TraceIDLabel] = span.SpanContext().TraceID().String()
	}
	return out
}

// BuildEnvVars converts an artifact's env var map and secret references into
// Kubernetes EnvVar slice. Plain env vars use literal values; secret refs use
// SecretKeyRef to inject values from Kubernetes Secrets at runtime.
func BuildEnvVars(artifact *api.Artifact) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	for k, v := range artifact.EnvVars {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}
	for envName, ref := range artifact.SecretRefs {
		parts := strings.SplitN(ref, ":", 2)
		envVars = append(envVars, corev1.EnvVar{
			Name: envName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: parts[0]},
					Key:                  parts[1],
				},
			},
		})
	}
	return envVars
}

// StaticFileVolumes returns the volume mounts and volumes needed to serve
// static files from a ConfigMap via nginx.
func StaticFileVolumes(configMapName string) ([]corev1.VolumeMount, []corev1.Volume) {
	mounts := []corev1.VolumeMount{
		{Name: "static-files", MountPath: "/usr/share/nginx/html"},
		{Name: "nginx-conf", MountPath: "/etc/nginx/conf.d/default.conf", SubPath: "nginx.conf"},
	}
	volumes := []corev1.Volume{
		{
			Name: "static-files",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
				},
			},
		},
		{
			Name: "nginx-conf",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
					Items:                []corev1.KeyToPath{{Key: "nginx.conf", Path: "nginx.conf"}},
				},
			},
		},
	}
	return mounts, volumes
}

// HardenedPodSecurityContext returns a pod-level SecurityContext that satisfies
// PodSecurity Standards "restricted". Workloads under PSA restricted otherwise
// produce admission warnings or are outright rejected on K8s 1.25+.
func HardenedPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptrBool(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// HardenedContainerSecurityContext returns a container-level SecurityContext
// that satisfies PodSecurity Standards "restricted". readOnlyRootFilesystem is
// intentionally NOT set — many user images (notably stock nginx) need to write
// to /var/cache or /var/run and break with it; callers that know their image
// is read-only-friendly can set it themselves.
func HardenedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptrBool(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

func ptrBool(b bool) *bool { return &b }

// FetchPodLogs retrieves log lines from the first pod matching the given label selector.
// If container is non-empty, logs are scoped to that container.
func FetchPodLogs(ctx context.Context, clientset kubernetes.Interface, namespace, labelSelector, container string, lines int) ([]string, error) {
	tailLines := int64(lines)
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return nil, nil
	}

	logOpts := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}
	if container != "" {
		logOpts.Container = container
	}

	pod := pods.Items[0]
	req := clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, logOpts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("streaming logs: %w", err)
	}
	defer stream.Close()

	var logLines []string
	scanner := bufio.NewScanner(stream)
	// Tolerate long log lines: the default 64KB scanner buffer triggers
	// bufio.ErrTooLong and silently truncates. Mirror the streaming path
	// (internal/deploy/logs.go).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		logLines = append(logLines, scanner.Text())
	}
	return logLines, scanner.Err()
}
