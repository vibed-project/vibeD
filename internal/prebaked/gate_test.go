package prebaked

import (
	"reflect"
	"testing"
)

func TestRegistryHasStandardLanguages(t *testing.T) {
	py, ok := ForLanguage("python")
	if !ok {
		t.Fatal("ForLanguage(python) not found — embedded manifest missing")
	}
	if !py.Has("flask") || !py.Has("fastapi") {
		t.Errorf("python manifest missing expected packages: %+v", py.Packages)
	}
	node, ok := ForLanguage("nodejs")
	if !ok {
		t.Fatal("ForLanguage(nodejs) not found — embedded manifest missing")
	}
	if !node.Has("express") {
		t.Errorf("nodejs manifest missing express: %+v", node.Packages)
	}
	if _, ok := ForLanguage("rust"); ok {
		t.Error("ForLanguage(rust) should not be found")
	}
}

func TestParseRequirements(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		wantDeps       []string
		wantAnalyzable bool
	}{
		{"simple names", "flask\nrequests\n", []string{"flask", "requests"}, true},
		{"version specifiers", "flask==2.0\nrequests>=2.1\njinja2~=3.0", []string{"flask", "requests", "jinja2"}, true},
		{"comments and blanks", "# deps\nflask\n\n  # indented\nrequests # inline", []string{"flask", "requests"}, true},
		{"extras and markers", "flask[async]\nuvicorn ; python_version > '3.8'", []string{"flask", "uvicorn"}, true},
		{"option line is unanalyzable", "flask\n-r dev.txt", []string{"flask"}, false},
		{"vcs install is unanalyzable", "git+https://example.com/pkg.git", nil, false},
		{"local path is unanalyzable", "./local-pkg", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, analyzable := ParseRequirements(tt.content)
			if analyzable != tt.wantAnalyzable {
				t.Errorf("analyzable = %v, want %v", analyzable, tt.wantAnalyzable)
			}
			if !reflect.DeepEqual(deps, tt.wantDeps) {
				t.Errorf("deps = %v, want %v", deps, tt.wantDeps)
			}
		})
	}
}

func TestParsePackageJSON(t *testing.T) {
	deps, err := ParsePackageJSON(`{"dependencies":{"express":"^4","cors":"^2"},"devDependencies":{"jest":"^29"}}`)
	if err != nil {
		t.Fatalf("ParsePackageJSON: %v", err)
	}
	// devDependencies are excluded; result is sorted.
	if !reflect.DeepEqual(deps, []string{"cors", "express"}) {
		t.Errorf("deps = %v, want [cors express]", deps)
	}
	if _, err := ParsePackageJSON("{not json"); err == nil {
		t.Error("ParsePackageJSON should error on invalid JSON")
	}
}

func TestEligiblePython(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		wantOK    bool
		wantMiss0 string // first missing entry, if any
	}{
		{"no requirements file", map[string]string{"app.py": "x"}, true, ""},
		{"all pre-baked", map[string]string{"requirements.txt": "flask\nrequests\n"}, true, ""},
		{"normalized name", map[string]string{"requirements.txt": "Flask==3.0\n"}, true, ""},
		{"missing dep", map[string]string{"requirements.txt": "flask\ndjango\n"}, false, "django"},
		{"unanalyzable", map[string]string{"requirements.txt": "-r other.txt\n"}, false, "requirements.txt has lines that cannot be analysed (-r/-e, VCS, URL or path installs)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, missing := Eligible("python", tt.files)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v (missing=%v)", ok, tt.wantOK, missing)
			}
			if tt.wantMiss0 != "" && (len(missing) == 0 || missing[0] != tt.wantMiss0) {
				t.Errorf("missing = %v, want first entry %q", missing, tt.wantMiss0)
			}
		})
	}
}

func TestEligibleNodejs(t *testing.T) {
	if ok, _ := Eligible("nodejs", map[string]string{"index.js": "x"}); !ok {
		t.Error("nodejs app with no package.json should be eligible")
	}
	if ok, _ := Eligible("nodejs", map[string]string{"package.json": `{"dependencies":{"express":"^4"}}`}); !ok {
		t.Error("nodejs app depending only on express should be eligible")
	}
	ok, missing := Eligible("nodejs", map[string]string{"package.json": `{"dependencies":{"lodash":"^4"}}`})
	if ok || len(missing) == 0 || missing[0] != "lodash" {
		t.Errorf("nodejs app depending on lodash should be ineligible; got ok=%v missing=%v", ok, missing)
	}
}

func TestEligibleUnsupportedLanguage(t *testing.T) {
	if ok, _ := Eligible("go", map[string]string{"main.go": "package main"}); ok {
		t.Error("go should not be fast-path eligible")
	}
	if ok, _ := Eligible("static", map[string]string{"index.html": "x"}); ok {
		t.Error("static should not be fast-path eligible (it has its own shortcut)")
	}
}
