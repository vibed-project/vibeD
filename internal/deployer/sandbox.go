package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vibed-project/vibeD/pkg/api"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"go.opentelemetry.io/otel/trace"
)

// SandboxDeployer implements the Deployer interface using agent-sandbox CRDs.
type SandboxDeployer struct {
	dynamicClient dynamic.Interface
	k8sClient     kubernetes.Interface
	logger        *slog.Logger
}

// NewSandboxDeployer creates a new deployer for Agent Sandbox targets.
func NewSandboxDeployer(dynamicClient dynamic.Interface, k8sClient kubernetes.Interface, logger *slog.Logger) *SandboxDeployer {
	return &SandboxDeployer{
		dynamicClient: dynamicClient,
		k8sClient:     k8sClient,
		logger:        logger,
	}
}

// sandboxGVR is the GroupVersionResource for the Sandbox CRD
var sandboxGVR = schema.GroupVersionResource{
	Group:    "agents.x-k8s.io",
	Version:  "v1alpha1",
	Resource: "sandboxes",
}

func (s *SandboxDeployer) buildSandboxObj(ctx context.Context, artifact *api.Artifact) *unstructured.Unstructured {
	// Build the basic pod containers block. SecurityContext fields below
	// satisfy PodSecurity Standards "restricted"; keep these in sync with
	// HardenedContainerSecurityContext() in helpers.go.
	container := map[string]interface{}{
		"name":  "user-container",
		"image": artifact.ImageRef,
		"securityContext": map[string]interface{}{
			"allowPrivilegeEscalation": false,
			"capabilities": map[string]interface{}{
				"drop": []interface{}{"ALL"},
			},
		},
	}

	// Add port if specified
	if artifact.Port > 0 {
		container["ports"] = []interface{}{
			map[string]interface{}{
				"containerPort": int64(artifact.Port),
			},
		}
	}

	// Add Env vars
	envVars := make([]interface{}, 0, len(artifact.EnvVars)+len(artifact.SecretRefs))
	for k, v := range artifact.EnvVars {
		envVars = append(envVars, map[string]interface{}{
			"name":  k,
			"value": v,
		})
	}
	for k, ref := range artifact.SecretRefs {
		parts := strings.SplitN(ref, ":", 2)
		envVars = append(envVars, map[string]interface{}{
			"name": k,
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": parts[0],
					"key":  parts[1],
				},
			},
		})
	}
	if len(envVars) > 0 {
		container["env"] = envVars
	}

	labels := map[string]interface{}{
		"app.kubernetes.io/name":       artifact.Name,
		"app.kubernetes.io/instance":   artifact.Name,
		"app.kubernetes.io/managed-by": "vibed",
		"vibed.dev/artifact-id":        artifact.ID,
	}
	if artifact.OwnerID != "" {
		labels["vibed.dev/owner-id"] = artifact.OwnerID
	}
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		labels[TraceIDLabel] = span.SpanContext().TraceID().String()
	}

	// Build the full CRD
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      artifact.Name,
				"namespace": artifact.Namespace,
				"labels":    labels,
			},
			"spec": map[string]interface{}{
				"podTemplate": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{
							container,
						},
						"securityContext": map[string]interface{}{
							"runAsNonRoot": true,
							"seccompProfile": map[string]interface{}{
								"type": "RuntimeDefault",
							},
						},
					},
				},
			},
		},
	}
	return obj
}

func (s *SandboxDeployer) Deploy(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	s.logger.Info("deploying Sandbox", "name", artifact.Name, "namespace", artifact.Namespace)

	obj := s.buildSandboxObj(ctx, artifact)
	_, err := s.dynamicClient.Resource(sandboxGVR).Namespace(artifact.Namespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating Sandbox: %w", err)
	}

	url, err := s.GetURL(ctx, artifact)
	if err != nil {
		// Log but don't fail deploy if URL isn't ready yet
		s.logger.Warn("could not determine URL for new Sandbox", "name", artifact.Name, "error", err)
	}

	return &DeployResult{URL: url}, nil
}

func (s *SandboxDeployer) Update(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	s.logger.Info("updating Sandbox", "name", artifact.Name, "namespace", artifact.Namespace)

	// Apply with merge patch instead of full Update so we don't blow away
	// controller-set defaults / status that aren't reflected in our spec.
	// We patch only the fields we own (image, env, port, labels) — the
	// agent-sandbox controller fills in the rest.
	obj := s.buildSandboxObj(ctx, artifact)
	patch, err := obj.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshaling Sandbox patch: %w", err)
	}

	_, err = s.dynamicClient.Resource(sandboxGVR).
		Namespace(artifact.Namespace).
		Patch(ctx, artifact.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patching Sandbox: %w", err)
	}

	url, _ := s.GetURL(ctx, artifact)
	return &DeployResult{URL: url}, nil
}

func (s *SandboxDeployer) Delete(ctx context.Context, artifact *api.Artifact) error {
	s.logger.Info("deleting Sandbox", "name", artifact.Name, "namespace", artifact.Namespace)
	
	err := s.dynamicClient.Resource(sandboxGVR).Namespace(artifact.Namespace).Delete(ctx, artifact.Name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting Sandbox: %w", err)
	}
	return nil
}

func (s *SandboxDeployer) GetURL(ctx context.Context, artifact *api.Artifact) (string, error) {
	// Since Sandboxes are like VMs, they are exposed by their stable hostname via DNS.
	// This generates the internal cluster address.
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", artifact.Name, artifact.Namespace, artifact.Port), nil
}

func (s *SandboxDeployer) GetLogs(ctx context.Context, artifact *api.Artifact, lines int) ([]string, error) {
	// Sandbox creates an underlying pod. We can query the K8s pod list by matching our labels.
	labelSelector := fmt.Sprintf("app.kubernetes.io/name=%s", artifact.Name)
	return FetchPodLogs(ctx, s.k8sClient, artifact.Namespace, labelSelector, "", lines)
}
