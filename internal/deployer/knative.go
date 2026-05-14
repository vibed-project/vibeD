package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/pkg/api"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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
	domainSuffix string
	gatewayPort  int
	readyTimeout time.Duration
	logger       *slog.Logger
}

// NewKnativeDeployer creates a new KnativeDeployer.
func NewKnativeDeployer(
	knClient knversioned.Interface,
	k8sClientset kubernetes.Interface,
	knCfg config.KnativeConfig,
	readyTimeout time.Duration,
	logger *slog.Logger,
) *KnativeDeployer {
	if readyTimeout <= 0 {
		readyTimeout = 10 * time.Minute
	}
	return &KnativeDeployer{
		knClient:     knClient,
		k8sClientset: k8sClientset,
		domainSuffix: knCfg.DomainSuffix,
		gatewayPort:  knCfg.GatewayPort,
		readyTimeout: readyTimeout,
		logger:       logger,
	}
}

func (d *KnativeDeployer) waitForReady(ctx context.Context, namespace, name string) error {
	d.logger.Info("waiting for Knative Service to become ready", "name", name, "timeout", d.readyTimeout)

	waitCtx, cancel := context.WithTimeout(ctx, d.readyTimeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timeout waiting for Knative Service %q to be ready: %w", name, waitCtx.Err())
		case <-ticker.C:
			svc, err := d.knClient.ServingV1().Services(namespace).Get(waitCtx, name, metav1.GetOptions{})
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
	ksvc := d.buildService(ctx, artifact)

	d.logger.Info("creating Knative Service",
		"name", artifact.Name,
		"namespace", artifact.Namespace,
		"image", artifact.ImageRef,
		)

		_, err := d.knClient.ServingV1().Services(artifact.Namespace).Create(ctx, ksvc, metav1.CreateOptions{})
		if err != nil {
		return nil, fmt.Errorf("creating Knative Service: %w", err)
	}

	if err := d.waitForReady(ctx, artifact.Namespace, artifact.Name); err != nil {
	        return nil, err
	}

	// Refetch to get the latest status URL after readiness
	readySvc, err := d.knClient.ServingV1().Services(artifact.Namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting Knative Service after ready: %w", err)
	}

	url := d.resolveURL(readySvc)
	d.logger.Info("Knative Service created and ready", "name", artifact.Name, "url", url)

	return &DeployResult{URL: url}, nil
}

func (d *KnativeDeployer) Update(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	// In-place spec update with retry on conflict. Knative rolls a new
	// revision automatically; no delete-then-recreate (which causes a 503
	// window). Rebuilding the full spec via buildService also handles
	// static deploys where the ConfigMap-backed Volumes change.
	desired := d.buildService(ctx, artifact)

	for attempt := 0; attempt < 3; attempt++ {
		existing, err := d.knClient.ServingV1().Services(artifact.Namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("getting existing Knative Service: %w", err)
		}
		// Preserve immutable fields from existing, copy desired spec into it.
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		_, err = d.knClient.ServingV1().Services(artifact.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
		if err == nil {
			break
		}
		if k8serrors.IsConflict(err) {
			d.logger.Debug("Knative Service update conflict, retrying", "name", artifact.Name, "attempt", attempt)
			continue
		}
		return nil, fmt.Errorf("updating Knative Service: %w", err)
	}

	if err := d.waitForReady(ctx, artifact.Namespace, artifact.Name); err != nil {
	        return nil, err
	}

	readySvc, err := d.knClient.ServingV1().Services(artifact.Namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting Knative Service after ready: %w", err)
	}

	url := d.resolveURL(readySvc)
	return &DeployResult{URL: url}, nil
}

func (d *KnativeDeployer) Delete(ctx context.Context, artifact *api.Artifact) error {
	d.logger.Info("deleting Knative Service", "name", artifact.Name)
	err := d.knClient.ServingV1().Services(artifact.Namespace).Delete(ctx, artifact.Name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting Knative Service: %w", err)
	}
	// Clean up static ConfigMap if present
	if artifact.StaticFiles != "" {
	        _ = d.k8sClientset.CoreV1().ConfigMaps(artifact.Namespace).Delete(ctx, artifact.StaticFiles, metav1.DeleteOptions{})
	}
	return nil
}

func (d *KnativeDeployer) GetURL(ctx context.Context, artifact *api.Artifact) (string, error) {
	svc, err := d.knClient.ServingV1().Services(artifact.Namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting Knative Service: %w", err)
	}
	return d.resolveURL(svc), nil
}

func (d *KnativeDeployer) GetLogs(ctx context.Context, artifact *api.Artifact, lines int) ([]string, error) {
	selector := fmt.Sprintf("serving.knative.dev/service=%s", artifact.Name)
	logLines, err := FetchPodLogs(ctx, d.k8sClientset, artifact.Namespace, selector, "user-container", lines)
	if err != nil {
		return nil, err
	}
	if logLines == nil {
		return []string{"(no pods found - service may be scaled to zero)"}, nil
	}
	return logLines, nil
}

func (d *KnativeDeployer) buildService(ctx context.Context, artifact *api.Artifact) *knservingv1.Service {
	containers := []corev1.Container{
		{
			Image:           artifact.ImageRef,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             BuildEnvVars(artifact),
			SecurityContext: HardenedContainerSecurityContext(),
		},
	}

	// Always declare the containerPort so Knative's queue-proxy probes the
	// right port instead of guessing. Default to 8080 when the caller didn't
	// specify one — guessing has caused readiness-probe timeouts on first
	// deploy.
	port := int32(artifact.Port)
	if port <= 0 {
		port = 8080
	}
	containers[0].Ports = []corev1.ContainerPort{{ContainerPort: port}}

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
			Namespace: artifact.Namespace,
			Labels:    VibedLabels(ctx, artifact),
		},
		Spec: knservingv1.ServiceSpec{
			ConfigurationSpec: knservingv1.ConfigurationSpec{
				Template: knservingv1.RevisionTemplateSpec{
					Spec: knservingv1.RevisionSpec{
						PodSpec: corev1.PodSpec{
							Containers:      containers,
							Volumes:         volumes,
							SecurityContext: HardenedPodSecurityContext(),
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
