package pool

import (
	"reflect"
	"testing"

	"github.com/vibed-project/vibeD/internal/config"
)

func TestResourcesSpec(t *testing.T) {
	tests := []struct {
		name string
		in   config.RunnerResources
		want map[string]interface{}
	}{
		{
			"all fields set",
			config.RunnerResources{
				Limits:   config.ResourceList{CPU: "500m", Memory: "512Mi"},
				Requests: config.ResourceList{CPU: "100m", Memory: "128Mi"},
			},
			map[string]interface{}{
				"limits":   map[string]interface{}{"cpu": "500m", "memory": "512Mi"},
				"requests": map[string]interface{}{"cpu": "100m", "memory": "128Mi"},
			},
		},
		{
			"only limits",
			config.RunnerResources{Limits: config.ResourceList{Memory: "1Gi"}},
			map[string]interface{}{
				"limits": map[string]interface{}{"memory": "1Gi"},
			},
		},
		{
			"empty → omitted",
			config.RunnerResources{},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resourcesSpec(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resourcesSpec() = %v, want %v", got, tt.want)
			}
		})
	}
}

// runnerContainer pulls the (single) container map out of a Sandbox CR built by
// buildSandbox. Tests use this to assert against the rendered container spec.
func runnerContainer(t *testing.T, obj map[string]interface{}) map[string]interface{} {
	t.Helper()
	spec, ok := obj["spec"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing spec: %+v", obj)
	}
	pt, ok := spec["podTemplate"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing podTemplate: %+v", spec)
	}
	pspec, ok := pt["spec"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing podTemplate.spec: %+v", pt)
	}
	containers, ok := pspec["containers"].([]interface{})
	if !ok || len(containers) != 1 {
		t.Fatalf("expected exactly 1 container, got %v", containers)
	}
	c, ok := containers[0].(map[string]interface{})
	if !ok {
		t.Fatalf("container is not a map: %T", containers[0])
	}
	return c
}

func TestBuildSandboxIncludesResources(t *testing.T) {
	rc := config.RunnerConfig{
		Image: "vibed-runner-python:dev", PoolSize: 2, ControlPort: 9000, AppPort: 8080,
		Resources: config.RunnerResources{
			Limits:   config.ResourceList{CPU: "500m", Memory: "512Mi"},
			Requests: config.ResourceList{CPU: "100m", Memory: "128Mi"},
		},
	}
	obj := buildSandbox("vibed-runner-python-x", "vibed-runners", "python", "tok", rc)
	c := runnerContainer(t, obj.Object)

	res, ok := c["resources"].(map[string]interface{})
	if !ok {
		t.Fatalf("container has no resources block: %+v", c)
	}
	if got := res["limits"].(map[string]interface{})["memory"]; got != "512Mi" {
		t.Errorf("limits.memory = %v, want 512Mi", got)
	}
	if got := res["requests"].(map[string]interface{})["cpu"]; got != "100m" {
		t.Errorf("requests.cpu = %v, want 100m", got)
	}
}

func TestBuildSandboxOmitsResourcesWhenUnset(t *testing.T) {
	rc := config.RunnerConfig{Image: "x", PoolSize: 1, ControlPort: 9000, AppPort: 8080}
	obj := buildSandbox("x", "ns", "python", "", rc)
	c := runnerContainer(t, obj.Object)
	if _, has := c["resources"]; has {
		t.Errorf("resources should be omitted when unset, got %v", c["resources"])
	}
}
