// Package templates_test validates every templates/<name>/template.yaml in
// the repo against the schema of kubernetes-sigs/agent-sandbox v0.4.5+. The
// production guarantee is "what we ship as a template applies cleanly to a
// real cluster" — but standing up a cluster in unit tests is overkill, so
// we instead parse each YAML, decode the documents, and assert the
// load-bearing fields are present and well-typed.
//
// The check is intentionally narrow. It is not a substitute for
// `kubectl apply --dry-run=server` on a real cluster; it is the cheapest
// guard against typos that catches 90 % of the mistakes (wrong apiVersion,
// missing replicas, image left blank) at `go test` time.
package templates_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// findTemplateFiles returns every template.yaml in the repo. The test is
// run from internal/templates, so we walk up two levels to reach the repo
// root.
func findTemplateFiles(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..", "templates")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name(), "template.yaml")
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no template.yaml files found under %s", root)
	}
	return out
}

// splitYAML returns the individual documents in a multi-doc YAML stream.
// Empty docs (e.g. trailing `---`) are skipped.
func splitYAML(s string) []string {
	parts := strings.Split(s, "\n---")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "---")
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// minimal is the only shape the validator needs — typed enough to fail on
// missing fields without pulling in the full agent-sandbox Go types.
type minimal struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec map[string]any `json:"spec"`
}

func TestTemplateManifestsAreWellFormed(t *testing.T) {
	for _, path := range findTemplateFiles(t) {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			docs := splitYAML(string(raw))
			if len(docs) < 2 {
				t.Fatalf("template must contain at least SandboxTemplate + SandboxWarmPool documents; got %d", len(docs))
			}

			seen := map[string]bool{}
			for _, doc := range docs {
				var m minimal
				if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
					t.Fatalf("unmarshal doc: %v\n---\n%s", err, doc)
				}
				// Skip header/comment-only chunks the splitter produces
				// when the file opens with a comment block. A real
				// manifest always sets apiVersion + kind.
				if m.APIVersion == "" && m.Kind == "" {
					continue
				}
				if m.APIVersion != "extensions.agents.x-k8s.io/v1alpha1" {
					t.Errorf("apiVersion=%q want extensions.agents.x-k8s.io/v1alpha1", m.APIVersion)
				}
				if m.Metadata.Name == "" {
					t.Errorf("metadata.name is required")
				}
				switch m.Kind {
				case "SandboxTemplate":
					assertSandboxTemplate(t, m)
				case "SandboxWarmPool":
					assertSandboxWarmPool(t, m)
				default:
					t.Errorf("unexpected kind %q", m.Kind)
				}
				seen[m.Kind] = true
			}
			if !seen["SandboxTemplate"] {
				t.Errorf("template bundle missing a SandboxTemplate document")
			}
			if !seen["SandboxWarmPool"] {
				t.Errorf("template bundle missing a SandboxWarmPool document")
			}
		})
	}
}

func assertSandboxTemplate(t *testing.T, m minimal) {
	t.Helper()
	pt, _ := m.Spec["podTemplate"].(map[string]any)
	if pt == nil {
		t.Errorf("SandboxTemplate %q: spec.podTemplate is required", m.Metadata.Name)
		return
	}
	spec, _ := pt["spec"].(map[string]any)
	containers, _ := spec["containers"].([]any)
	if len(containers) == 0 {
		t.Errorf("SandboxTemplate %q: spec.podTemplate.spec.containers must have at least one entry", m.Metadata.Name)
		return
	}
	c0, _ := containers[0].(map[string]any)
	image, _ := c0["image"].(string)
	if image == "" {
		t.Errorf("SandboxTemplate %q: container 0 must set image", m.Metadata.Name)
	}
}

func assertSandboxWarmPool(t *testing.T, m minimal) {
	t.Helper()
	rep, ok := m.Spec["replicas"].(float64)
	if !ok || rep < 0 {
		t.Errorf("SandboxWarmPool %q: spec.replicas must be a non-negative integer (got %v)", m.Metadata.Name, m.Spec["replicas"])
	}
	ref, _ := m.Spec["sandboxTemplateRef"].(map[string]any)
	if ref == nil {
		t.Errorf("SandboxWarmPool %q: spec.sandboxTemplateRef is required", m.Metadata.Name)
		return
	}
	if n, _ := ref["name"].(string); n == "" {
		t.Errorf("SandboxWarmPool %q: spec.sandboxTemplateRef.name is required", m.Metadata.Name)
	}
}
