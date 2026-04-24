package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/pkg/api"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"knative.dev/pkg/apis"
	knservingv1 "knative.dev/serving/pkg/apis/serving/v1"
	knversioned "knative.dev/serving/pkg/client/clientset/versioned"
)

// KnativeDeployer deploys artifacts as Knative Services.
type KnativeDeployer struct {
	knClient     knversioned.Interface
	k8sClientset kubernetes.Interface
	namespace    string
	domainSuffix string
	gatewayPort  int
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
		gatewayPort:  knCfg.GatewayPort,
		logger:       logger,
	}
}

func (d *KnativeDeployer) waitForReady(ctx context.Context, name string) error {
	d.logger.Info("waiting for Knative Service to become ready", "name", name)
	
	// Ensure we don't wait forever if the context doesn't have a deadline
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timeout waiting for Knative Service %q to be ready: %w", name, waitCtx.Err())
		case <-ticker.C:
			svc, err := d.knClient.ServingV1().Services(d.namespace).Get(waitCtx, name, metav1.GetOptions{})
			if err != nil {
				d.logger.Debug("error getting Knative Service status", "name", name, "error", err)
				continue
			}

			cond := svc.Status.GetCondition(apis.ConditionReady)
			if cond != nil {
				if cond.IsTrue() {
					return nil // Ready!
				}
				if cond.IsFalse() && cond.Reason != "Deploying" && cond.Reason != "Resolving" && cond.Reason != "RevisionMissing" {
					// It's in a failing state. Return early to avoid waiting for timeout.
					return fmt.Errorf("Knative Service %q failed to become ready: %s - %s", name, cond.Reason, cond.Message)
				}
			}
		}
	}
}

func (d *KnativeDeployer) Deploy(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	ksvc := d.buildService(artifact)

	d.logger.Info("creating Knative Service",
		"name", artifact.Name,
		"namespace", d.namespace,
		"image", artifact.ImageRef,
	)

	_, err := d.knClient.ServingV1().Services(d.namespace).Create(ctx, ksvc, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating Knative Service: %w", err)
	}

	if err := d.waitForReady(ctx, artifact.Name); err != nil {
		return nil, err
	}

	// Refetch to get the latest status URL after readiness
	readySvc, err := d.knClient.ServingV1().Services(d.namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting Knative Service after ready: %w", err)
	}

	url := d.resolveURL(readySvc)
	d.logger.Info("Knative Service created and ready", "name", artifact.Name, "url", url)

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
	existing.Spec.Template.Spec.Containers[0].Env = BuildEnvVars(artifact)
	if artifact.Port > 0 {
		existing.Spec.Template.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: int32(artifact.Port)}}
	}

	_, err = d.knClient.ServingV1().Services(d.namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("updating Knative Service: %w", err)
	}

	if err := d.waitForReady(ctx, artifact.Name); err != nil {
		return nil, err
	}

	readySvc, err := d.knClient.ServingV1().Services(d.namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting Knative Service after ready: %w", err)
	}

	url := d.resolveURL(readySvc)
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
	selector := fmt.Sprintf("serving.knative.dev/service=%s", artifact.Name)
	logLines, err := FetchPodLogs(ctx, d.k8sClientset, d.namespace, selector, "user-container", lines)
	if err != nil {
		return nil, err
	}
	if logLines == nil {
		return []string{"(no pods found - service may be scaled to zero)"}, nil
	}
	return logLines, nil
}

func (d *KnativeDeployer) buildService(artifact *api.Artifact) *knservingv1.Service {
	containers := []corev1.Container{
		{
			Image:           artifact.ImageRef,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             BuildEnvVars(artifact),
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
		mounts, vols := StaticFileVolumes(artifact.StaticFiles)
		containers[0].VolumeMounts = mounts
		volumes = vols
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

func (d *KnativeDeployer) resolveURL(svc *knservingv1.Service) string {
	var host string
	if svc.Status.URL != nil {
		host = svc.Status.URL.Host
	} else {
		host = fmt.Sprintf("%s.%s.%s", svc.Name, svc.Namespace, d.domainSuffix)
	}
	if d.gatewayPort > 0 && d.gatewayPort != 80 {
		return fmt.Sprintf("http://%s:%d", host, d.gatewayPort)
	}
	return fmt.Sprintf("http://%s", host)
}
