// Package prebaked describes which dependencies are baked into a runner image.
//
// The fast-path dependency gate (Phase 3) loads one Manifest per language and
// rejects any app whose declared dependencies are not all pre-installed —
// those fall back to the full container build. Keeping the manifest in a file
// that ships next to the runner image's Dockerfile means the gate's view of
// "what's available" stays in lockstep with what the image actually contains.
package prebaked

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is the set of dependencies pre-installed in one runner image.
type Manifest struct {
	// Language is the appspec language this image serves (e.g. "python").
	Language string `yaml:"language"`
	// Image is the runner image name, e.g. "vibed-runner-python".
	Image string `yaml:"image"`
	// Runtime is the base image / interpreter version. Informational only.
	Runtime string `yaml:"runtime"`
	// Packages lists the pre-installed dependency names (no version specifiers).
	Packages []string `yaml:"packages"`

	index map[string]struct{} // normalized lookup, built by Parse
}

// Load reads and parses a manifest YAML file.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading prebaked manifest: %w", err)
	}
	return Parse(data)
}

// Parse parses a manifest from YAML bytes and builds its lookup index.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing prebaked manifest: %w", err)
	}
	if m.Language == "" {
		return nil, fmt.Errorf("prebaked manifest: language is required")
	}
	m.index = make(map[string]struct{}, len(m.Packages))
	for _, p := range m.Packages {
		if n := normalize(p); n != "" {
			m.index[n] = struct{}{}
		}
	}
	return &m, nil
}

// Has reports whether dependency name is pre-baked into the image. Lookup is
// normalized: case-insensitive, with '_' and '.' treated as '-'. This matches
// PEP 503 name normalization for Python and is a harmless no-op for npm names.
func (m *Manifest) Has(name string) bool {
	if m == nil {
		return false
	}
	_, ok := m.index[normalize(name)]
	return ok
}

// Count returns the number of pre-baked packages.
func (m *Manifest) Count() int {
	if m == nil {
		return 0
	}
	return len(m.index)
}

func normalize(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.NewReplacer("_", "-", ".", "-").Replace(n)
}
