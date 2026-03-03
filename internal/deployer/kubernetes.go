package deployer

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"

	"github.com/maxkorbacher/vibed/internal/config"
	"github.com/maxkorbacher/vibed/pkg/api"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// KubernetesDeployer deploys artifacts as plain Kubernetes Deployments + Services.
type KubernetesDeployer struct {
	clientset kubernetes.Interface
	namespace string
	logger    *slog.Logger
}

// NewKubernetesDeployer creates a new KubernetesDeployer.
func NewKubernetesDeployer(
	clientset kubernetes.Interface,
	cfg config.DeploymentConfig,
	logger *slog.Logger,
) *KubernetesDeployer {
	return &KubernetesDeployer{
		clientset: clientset,
		namespace: cfg.Namespace,
		logger:    logger,
	}
}

func (d *KubernetesDeployer) Deploy(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	port := artifact.Port
	if port == 0 {
		port = 8080
	}

	labels := map[string]string{
		"app":                          artifact.Name,
		"app.kubernetes.io/managed-by": "vibed",
		"vibed.dev/artifact-id":        artifact.ID,
	}

	// Build container spec
	container := corev1.Container{
		Name:            "app",
		Image:           artifact.ImageRef,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports:           []corev1.ContainerPort{{ContainerPort: int32(port)}},
		Env:             buildEnvVars(artifact),
	}

	var volumes []corev1.Volume

	// Static files: mount ConfigMap as nginx html + config
	if artifact.StaticFiles != "" {
		container.VolumeMounts = []corev1.VolumeMount{
			{Name: "static-files", MountPath: "/usr/share/nginx/html"},
			{Name: "nginx-conf", MountPath: "/etc/nginx/conf.d/default.conf", SubPath: "nginx.conf"},
		}
		volumes = []corev1.Volume{
			{
				Name: "static-files",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: artifact.StaticFiles},
					},
				},
			},
			{
				Name: "nginx-conf",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: artifact.StaticFiles},
						Items:                []corev1.KeyToPath{{Key: "nginx.conf", Path: "nginx.conf"}},
					},
				},
			},
		}
	}

	// Create Deployment
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      artifact.Name,
			Namespace: d.namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": artifact.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}

	d.logger.Info("creating Kubernetes Deployment", "name", artifact.Name, "namespace", d.namespace)
	_, err := d.clientset.AppsV1().Deployments(d.namespace).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating Deployment: %w", err)
	}

	// Create Service with NodePort
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      artifact.Name,
			Namespace: d.namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: map[string]string{"app": artifact.Name},
			Ports: []corev1.ServicePort{
				{
					Port:       int32(port),
					TargetPort: intstr.FromInt32(int32(port)),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	createdSvc, err := d.clientset.CoreV1().Services(d.namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating Service: %w", err)
	}

	url := d.resolveURL(createdSvc)
	d.logger.Info("Kubernetes Deployment created", "name", artifact.Name, "url", url)

	return &DeployResult{URL: url}, nil
}

func (d *KubernetesDeployer) Update(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	existing, err := d.clientset.AppsV1().Deployments(d.namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting existing Deployment: %w", err)
	}

	if len(existing.Spec.Template.Spec.Containers) == 0 {
		return nil, fmt.Errorf("Deployment %q has no containers", artifact.Name)
	}

	existing.Spec.Template.Spec.Containers[0].Image = artifact.ImageRef
	existing.Spec.Template.Spec.Containers[0].Env = buildEnvVars(artifact)

	_, err = d.clientset.AppsV1().Deployments(d.namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("updating Deployment: %w", err)
	}

	svc, err := d.clientset.CoreV1().Services(d.namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting Service: %w", err)
	}

	return &DeployResult{URL: d.resolveURL(svc)}, nil
}

func (d *KubernetesDeployer) Delete(ctx context.Context, artifact *api.Artifact) error {
	d.logger.Info("deleting Kubernetes Deployment", "name", artifact.Name)
	err := d.clientset.AppsV1().Deployments(d.namespace).Delete(ctx, artifact.Name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting Deployment: %w", err)
	}
	err = d.clientset.CoreV1().Services(d.namespace).Delete(ctx, artifact.Name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting Service: %w", err)
	}
	// Clean up static ConfigMap if present
	if artifact.StaticFiles != "" {
		_ = d.clientset.CoreV1().ConfigMaps(d.namespace).Delete(ctx, artifact.StaticFiles, metav1.DeleteOptions{})
	}
	return nil
}

func (d *KubernetesDeployer) GetURL(ctx context.Context, artifact *api.Artifact) (string, error) {
	svc, err := d.clientset.CoreV1().Services(d.namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting Service: %w", err)
	}
	return d.resolveURL(svc), nil
}

func (d *KubernetesDeployer) GetLogs(ctx context.Context, artifact *api.Artifact, lines int) ([]string, error) {
	tailLines := int64(lines)
	pods, err := d.clientset.CoreV1().Pods(d.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", artifact.Name),
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return []string{"(no pods found)"}, nil
	}

	pod := pods.Items[0]
	req := d.clientset.CoreV1().Pods(d.namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		TailLines: &tailLines,
	})
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

func buildEnvVars(artifact *api.Artifact) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	for k, v := range artifact.EnvVars {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}
	return envVars
}

func (d *KubernetesDeployer) resolveURL(svc *corev1.Service) string {
	for _, port := range svc.Spec.Ports {
		if port.NodePort > 0 {
			return fmt.Sprintf("http://localhost:%d", port.NodePort)
		}
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local", svc.Name, svc.Namespace)
}
