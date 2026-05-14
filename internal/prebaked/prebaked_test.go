package prebaked

import "testing"

const sampleManifest = `
language: python
image: vibed-runner-python
runtime: python:3.12-slim
packages:
  - Flask
  - fastapi
  - python_dateutil
`

func TestParseAndHas(t *testing.T) {
	m, err := Parse([]byte(sampleManifest))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Language != "python" || m.Image != "vibed-runner-python" {
		t.Fatalf("unexpected metadata: %+v", m)
	}
	if m.Count() != 3 {
		t.Fatalf("Count = %d, want 3", m.Count())
	}

	// Normalized lookups: case-insensitive, '_'/'.' folded to '-'.
	for _, name := range []string{"flask", "Flask", "FLASK", "fastapi", "python-dateutil", "python_dateutil"} {
		if !m.Has(name) {
			t.Errorf("Has(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"django", "numpy", ""} {
		if m.Has(name) {
			t.Errorf("Has(%q) = true, want false", name)
		}
	}
}

func TestParseRequiresLanguage(t *testing.T) {
	if _, err := Parse([]byte("image: x\npackages: [a]\n")); err == nil {
		t.Fatal("Parse without language should error")
	}
}

func TestParseInvalidYAML(t *testing.T) {
	if _, err := Parse([]byte("language: python\npackages: [unterminated")); err == nil {
		t.Fatal("Parse of invalid YAML should error")
	}
}

func TestNilManifestHasIsSafe(t *testing.T) {
	var m *Manifest
	if m.Has("anything") {
		t.Error("nil Manifest.Has should return false")
	}
	if m.Count() != 0 {
		t.Error("nil Manifest.Count should return 0")
	}
}
