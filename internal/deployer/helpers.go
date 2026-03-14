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
)

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
	for scanner.Scan() {
		logLines = append(logLines, scanner.Text())
	}
	return logLines, scanner.Err()
}
