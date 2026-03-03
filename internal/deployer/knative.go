package deployer

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"

	"github.com/maxkorbacher/vibed/internal/config"
	"github.com/maxkorbacher/vibed/pkg/api"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	knservingv1 "knative.dev/serving/pkg/apis/serving/v1"
	knversioned "knative.dev/serving/pkg/client/clientset/versioned"
)

// KnativeDeployer deploys artifacts as Knative Services.
type KnativeDeployer struct {
	knClient     knversioned.Interface
	k8sClientset kubernetes.Interface
	namespace    string
	domainSuffix string
	logger       *slog.Logger
}

// NewKnativeDeployer creates a new KnativeDeployer.
func NewKnativeDeployer(
	knClient knversioned.Interface,
	k8sClientset kubernetes.Interface,
	cfg config.DeploymentConfig,
	knCfg config.KnativeConfig,
	logger *slog.Logger,
) *KnativeDeployer {
	return &KnativeDeployer{
		knClient:     knClient,
		k8sClientset: k8sClientset,
		namespace:    cfg.Namespace,
		domainSuffix: knCfg.DomainSuffix,
		logger:       logger,
	}
}

func (d *KnativeDeployer) Deploy(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	ksvc := d.buildService(artifact)

	d.logger.Info("creating Knative Service",
		"name", artifact.Name,
		"namespace", d.namespace,
		"image", artifact.ImageRef,
	)

	created, err := d.knClient.ServingV1().Services(d.namespace).Create(ctx, ksvc, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating Knative Service: %w", err)
	}

	url := d.resolveURL(created)
	d.logger.Info("Knative Service created", "name", artifact.Name, "url", url)

	return &DeployResult{URL: url}, nil
}

func (d *KnativeDeployer) Update(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	// For static updates, recreate the service to pick up ConfigMap changes
	if artifact.StaticFiles != "" {
		_ = d.knClient.ServingV1().Services(d.namespace).Delete(ctx, artifact.Name, metav1.DeleteOptions{})
		return d.Deploy(ctx, artifact)
	}

	existing, err := d.knClient.ServingV1().Services(d.namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting existing Knative Service: %w", err)
	}

	if len(existing.Spec.Template.Spec.Containers) == 0 {
		return nil, fmt.Errorf("Knative Service %q has no containers", artifact.Name)
	}

	existing.Spec.Template.Spec.Containers[0].Image = artifact.ImageRef
	existing.Spec.Template.Spec.Containers[0].Env = d.buildEnvVars(artifact)
	if artifact.Port > 0 {
		existing.Spec.Template.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: int32(artifact.Port)}}
	}

	updated, err := d.knClient.ServingV1().Services(d.namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("updating Knative Service: %w", err)
	}

	url := d.resolveURL(updated)
	return &DeployResult{URL: url}, nil
}

func (d *KnativeDeployer) Delete(ctx context.Context, artifact *api.Artifact) error {
	d.logger.Info("deleting Knative Service", "name", artifact.Name)
	err := d.knClient.ServingV1().Services(d.namespace).Delete(ctx, artifact.Name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting Knative Service: %w", err)
	}
	// Clean up static ConfigMap if present
	if artifact.StaticFiles != "" {
		_ = d.k8sClientset.CoreV1().ConfigMaps(d.namespace).Delete(ctx, artifact.StaticFiles, metav1.DeleteOptions{})
	}
	return nil
}

func (d *KnativeDeployer) GetURL(ctx context.Context, artifact *api.Artifact) (string, error) {
	svc, err := d.knClient.ServingV1().Services(d.namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting Knative Service: %w", err)
	}
	return d.resolveURL(svc), nil
}

func (d *KnativeDeployer) GetLogs(ctx context.Context, artifact *api.Artifact, lines int) ([]string, error) {
	tailLines := int64(lines)
	pods, err := d.k8sClientset.CoreV1().Pods(d.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("serving.knative.dev/service=%s", artifact.Name),
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return []string{"(no pods found - service may be scaled to zero)"}, nil
	}

	pod := pods.Items[0]
	req := d.k8sClientset.CoreV1().Pods(d.namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		TailLines: &tailLines,
		Container: "user-container",
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

func (d *KnativeDeployer) buildService(artifact *api.Artifact) *knservingv1.Service {
	containers := []corev1.Container{
		{
			Image:           artifact.ImageRef,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             d.buildEnvVars(artifact),
		},
	}

	if artifact.Port > 0 {
		containers[0].Ports = []corev1.ContainerPort{
			{ContainerPort: int32(artifact.Port)},
		}
	}

	var volumes []corev1.Volume

	// Static files: mount ConfigMap as nginx html + config
	if artifact.StaticFiles != "" {
		containers[0].VolumeMounts = []corev1.VolumeMount{
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

	return &knservingv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      artifact.Name,
			Namespace: d.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "vibed",
				"vibed.dev/artifact-id":        artifact.ID,
			},
		},
		Spec: knservingv1.ServiceSpec{
			ConfigurationSpec: knservingv1.ConfigurationSpec{
				Template: knservingv1.RevisionTemplateSpec{
					Spec: knservingv1.RevisionSpec{
						PodSpec: corev1.PodSpec{
							Containers: containers,
							Volumes:    volumes,
						},
					},
				},
			},
		},
	}
}

func (d *KnativeDeployer) buildEnvVars(artifact *api.Artifact) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	for k, v := range artifact.EnvVars {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}
	return envVars
}

func (d *KnativeDeployer) resolveURL(svc *knservingv1.Service) string {
	if svc.Status.URL != nil {
		return svc.Status.URL.String()
	}
	// Construct URL from domain suffix if status URL not yet populated.
	return fmt.Sprintf("http://%s.%s.%s", svc.Name, svc.Namespace, d.domainSuffix)
}
