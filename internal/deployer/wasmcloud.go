package deployer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/pkg/api"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var applicationGVR = schema.GroupVersionResource{
	Group:    "core.oam.dev",
	Version:  "v1beta1",
	Resource: "applications",
}

const httpServerProviderImage = "ghcr.io/wasmcloud/http-server:0.26.0"

// WasmCloudDeployer deploys artifacts as wasmCloud OAM Applications
// via the wasmcloud-operator's Kubernetes CRDs.
type WasmCloudDeployer struct {
	dynamicClient dynamic.Interface
	k8sClientset  kubernetes.Interface
	namespace     string
	latticeID     string
	logger        *slog.Logger
}

// NewWasmCloudDeployer creates a new WasmCloudDeployer.
func NewWasmCloudDeployer(
	dynClient dynamic.Interface,
	k8sClientset kubernetes.Interface,
	cfg config.DeploymentConfig,
	wasmCfg config.WasmCloudConfig,
	logger *slog.Logger,
) *WasmCloudDeployer {
	latticeID := wasmCfg.LatticeID
	if latticeID == "" {
		latticeID = "default"
	}
	return &WasmCloudDeployer{
		dynamicClient: dynClient,
		k8sClientset:  k8sClientset,
		namespace:     cfg.Namespace,
		latticeID:     latticeID,
		logger:        logger,
	}
}

func (d *WasmCloudDeployer) Deploy(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	app := d.buildApplication(artifact)

	d.logger.Info("creating wasmCloud OAM Application",
		"name", artifact.Name,
		"namespace", d.namespace,
	)

	_, err := d.dynamicClient.Resource(applicationGVR).Namespace(d.namespace).Create(
		ctx, app, metav1.CreateOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("creating OAM Application: %w", err)
	}

	url := d.serviceURL(artifact.Name)
	return &DeployResult{URL: url}, nil
}

func (d *WasmCloudDeployer) Update(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	app := d.buildApplication(artifact)

	_, err := d.dynamicClient.Resource(applicationGVR).Namespace(d.namespace).Update(
		ctx, app, metav1.UpdateOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("updating OAM Application: %w", err)
	}

	url := d.serviceURL(artifact.Name)
	return &DeployResult{URL: url}, nil
}

func (d *WasmCloudDeployer) Delete(ctx context.Context, artifact *api.Artifact) error {
	d.logger.Info("deleting wasmCloud OAM Application", "name", artifact.Name)
	return d.dynamicClient.Resource(applicationGVR).Namespace(d.namespace).Delete(
		ctx, artifact.Name, metav1.DeleteOptions{},
	)
}

func (d *WasmCloudDeployer) GetURL(ctx context.Context, artifact *api.Artifact) (string, error) {
	// Try to resolve via the operator-created Kubernetes Service
	svc, err := d.k8sClientset.CoreV1().Services(d.namespace).Get(ctx, artifact.Name, metav1.GetOptions{})
	if err == nil && svc != nil {
		for _, port := range svc.Spec.Ports {
			if port.NodePort > 0 {
				return fmt.Sprintf("http://localhost:%d", port.NodePort), nil
			}
		}
	}
	return d.serviceURL(artifact.Name), nil
}

func (d *WasmCloudDeployer) GetLogs(ctx context.Context, artifact *api.Artifact, lines int) ([]string, error) {
	selector := fmt.Sprintf("app=%s", artifact.Name)
	logLines, err := FetchPodLogs(ctx, d.k8sClientset, d.namespace, selector, "", lines)
	if err != nil {
		return nil, err
	}
	if logLines == nil {
		return []string{"(no pods found for wasmCloud application)"}, nil
	}
	return logLines, nil
}

// serviceURL returns the cluster-local DNS URL for the wasmCloud service.
func (d *WasmCloudDeployer) serviceURL(name string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local", name, d.namespace)
}

// buildApplication creates a wadm-compatible OAM Application manifest.
func (d *WasmCloudDeployer) buildApplication(artifact *api.Artifact) *unstructured.Unstructured {
	port := artifact.Port
	if port == 0 {
		port = 8080
	}

	componentName := artifact.Name
	providerName := artifact.Name + "-httpserver"

	// Build wadm Application spec with component + HTTP provider + link
	spec := map[string]interface{}{
		"components": []interface{}{
			// The user's wasm component
			map[string]interface{}{
				"name": componentName,
				"type": "component",
				"properties": map[string]interface{}{
					"image": artifact.ImageRef,
				},
				"traits": []interface{}{
					map[string]interface{}{
						"type": "spreadscaler",
						"properties": map[string]interface{}{
							"instances": 1,
						},
					},
				},
			},
			// HTTP server capability provider
			map[string]interface{}{
				"name": providerName,
				"type": "capability",
				"properties": map[string]interface{}{
					"image": httpServerProviderImage,
				},
				"traits": []interface{}{
					map[string]interface{}{
						"type": "spreadscaler",
						"properties": map[string]interface{}{
							"instances": 1,
						},
					},
					map[string]interface{}{
						"type": "link",
						"properties": map[string]interface{}{
							"target":    componentName,
							"namespace": "wasi",
							"package":   "http",
							"interfaces": []interface{}{
								"incoming-handler",
							},
							"source_config": []interface{}{
								map[string]interface{}{
									"name": "default-http",
									"properties": map[string]interface{}{
										"address": fmt.Sprintf("0.0.0.0:%d", port),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	labels := map[string]interface{}{
		"app.kubernetes.io/managed-by": "vibed",
		"vibed.dev/artifact-id":        artifact.ID,
	}

	app := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "core.oam.dev/v1beta1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      artifact.Name,
				"namespace": d.namespace,
				"labels":    labels,
				"annotations": map[string]interface{}{
					"version":     "v0.0.1",
					"description": fmt.Sprintf("vibeD-managed artifact: %s", artifact.Name),
				},
			},
			"spec": spec,
		},
	}

	// Ensure valid JSON round-trip for unstructured
	data, _ := json.Marshal(app.Object)
	json.Unmarshal(data, &app.Object)

	return app
}
