package pool

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/vibed-project/vibeD/internal/config"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// sandboxGVR is the GroupVersionResource for the agent-sandbox CRD. Kept local
// to the pool package so it does not depend on internal/deployer.
var sandboxGVR = schema.GroupVersionResource{
	Group:    "agents.x-k8s.io",
	Version:  "v1alpha1",
	Resource: "sandboxes",
}

// Labels stamped on every runner Sandbox so operators (and a future GC sweep)
// can find pool-owned resources.
const (
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelComponent = "app.kubernetes.io/component"
	labelLanguage  = "vibed.dev/runner-language"

	managedByVibed      = "vibed"
	componentRunnerPool = "runner-pool"
)

// Runner is a warm runner pod (backed by a Sandbox CR) that vibeD injects user
// source into. Once handed out by Claim it is owned by the caller until Release.
type Runner struct {
	Name      string // Sandbox / Service / pod name
	Namespace string
	Language  string // appspec language
	Image     string
	CreatedAt time.Time

	controlPort int
	appPort     int
}

// ControlURL is the in-cluster base URL of the runner agent's control API.
// This port is never exposed through the Sandbox's public URL.
func (r *Runner) ControlURL() string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", r.Name, r.Namespace, r.controlPort)
}

// AppURL is the in-cluster base URL the user app serves on.
func (r *Runner) AppURL() string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", r.Name, r.Namespace, r.appPort)
}

// buildSandbox renders the Sandbox CR for a warm runner of the given language.
// The pod template mirrors the hardened securityContext used by the Sandbox
// deployer, and gates pod readiness on the agent's /healthz so the Sandbox
// Service only has endpoints once the control API is actually up. token, when
// non-empty, is injected as VIBED_AGENT_TOKEN so the agent authenticates the
// control API against the same secret the RunnerDeployer presents.
func buildSandbox(name, namespace, language, token string, rc config.RunnerConfig) *unstructured.Unstructured {
	container := map[string]interface{}{
		"name":  "runner",
		"image": rc.Image,
		"ports": []interface{}{
			map[string]interface{}{"name": "control", "containerPort": int64(rc.ControlPort)},
			map[string]interface{}{"name": "app", "containerPort": int64(rc.AppPort)},
		},
		"securityContext": map[string]interface{}{
			"allowPrivilegeEscalation": false,
			"capabilities": map[string]interface{}{
				"drop": []interface{}{"ALL"},
			},
		},
		"readinessProbe": map[string]interface{}{
			"httpGet": map[string]interface{}{
				"path": "/healthz",
				"port": int64(rc.ControlPort),
			},
			"periodSeconds": int64(2),
		},
	}
	if res := resourcesSpec(rc.Resources); res != nil {
		container["resources"] = res
	}
	if token != "" {
		container["env"] = []interface{}{
			map[string]interface{}{"name": "VIBED_AGENT_TOKEN", "value": token},
		}
	}

	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]interface{}{
				labelManagedBy: managedByVibed,
				labelComponent: componentRunnerPool,
				labelLanguage:  language,
			},
		},
		"spec": map[string]interface{}{
			"podTemplate": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{container},
					"securityContext": map[string]interface{}{
						"runAsNonRoot": true,
						"seccompProfile": map[string]interface{}{
							"type": "RuntimeDefault",
						},
					},
				},
			},
		},
	}}
}

// resourcesSpec renders RunnerResources into the K8s container `resources`
// block, omitting CPU/memory entries left empty. Returns nil when nothing is
// set so the field is left out of the spec entirely.
func resourcesSpec(r config.RunnerResources) map[string]interface{} {
	limits := resourceList(r.Limits)
	requests := resourceList(r.Requests)
	if limits == nil && requests == nil {
		return nil
	}
	out := map[string]interface{}{}
	if limits != nil {
		out["limits"] = limits
	}
	if requests != nil {
		out["requests"] = requests
	}
	return out
}

func resourceList(rl config.ResourceList) map[string]interface{} {
	if rl.CPU == "" && rl.Memory == "" {
		return nil
	}
	out := map[string]interface{}{}
	if rl.CPU != "" {
		out["cpu"] = rl.CPU
	}
	if rl.Memory != "" {
		out["memory"] = rl.Memory
	}
	return out
}

// httpProbe is the default readiness probe: a GET on the agent's /healthz.
// It is a field on Pool so tests can substitute it.
func httpProbe(ctx context.Context, controlURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, controlURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent /healthz returned %d", resp.StatusCode)
	}
	return nil
}
