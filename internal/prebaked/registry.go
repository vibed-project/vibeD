package prebaked

import (
	"embed"
	"fmt"
)

// manifestFS embeds the canonical pre-baked dependency manifests. The same
// files are copied into the runner images by their Dockerfiles, so the gate's
// view of "what's pre-baked" cannot drift from the images.
//
//go:embed manifests/*.yaml
var manifestFS embed.FS

// registry maps appspec language → its embedded manifest, populated by init.
var registry = map[string]*Manifest{}

func init() {
	entries, err := manifestFS.ReadDir("manifests")
	if err != nil {
		panic(fmt.Sprintf("prebaked: reading embedded manifests: %v", err))
	}
	for _, e := range entries {
		data, err := manifestFS.ReadFile("manifests/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("prebaked: reading embedded manifest %s: %v", e.Name(), err))
		}
		m, err := Parse(data)
		if err != nil {
			// A malformed embedded manifest is a build-time mistake — fail loud
			// at startup rather than silently disabling the fast path.
			panic(fmt.Sprintf("prebaked: parsing embedded manifest %s: %v", e.Name(), err))
		}
		registry[m.Language] = m
	}
}

// ForLanguage returns the embedded pre-baked manifest for the given appspec
// language, or (nil, false) when no runner image is known for it.
func ForLanguage(language string) (*Manifest, bool) {
	m, ok := registry[language]
	return m, ok
}

// Languages returns the languages that have an embedded runner manifest.
func Languages() []string {
	out := make([]string, 0, len(registry))
	for lang := range registry {
		out = append(out, lang)
	}
	return out
}
