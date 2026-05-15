package classifier

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"
	"time"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// makeTarball builds a deterministic gzipped tarball from a name → content
// map. Bytes of contents are written as-is; directories aren't materialized
// because the classifier only reads file entries.
func makeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
			ModTime:  time.Unix(0, 0),
		}); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gz: %v", err)
	}
	return buf.Bytes()
}

func classify(t *testing.T, files map[string]string) Decision {
	t.Helper()
	d, err := Classifier{}.Classify(context.Background(), bytes.NewReader(makeTarball(t, files)))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	return d
}

// TestNineRules walks each rule from refactor.md §5.2 in order. The cases
// are intentionally simple — the goal is to lock in the *priority* of the
// rules, not to exhaustively cover bundler variants.
func TestNineRules(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		wantRule int
		wantLane vibedv1.Lane
		wantTpl  string
	}{
		{
			name: "rule 1: static HTML only",
			files: map[string]string{
				"index.html":   "<h1>hi</h1>",
				"styles.css":   "body{}",
				"img/logo.svg": "<svg></svg>",
			},
			wantRule: RuleStaticOnly,
			wantLane: vibedv1.LaneFast,
			wantTpl:  TemplateStaticNginx,
		},
		{
			name: "rule 2: SPA with build script + vite dep",
			files: map[string]string{
				"index.html":   "<div id=root></div>",
				"src/main.tsx": "export {}",
				"package.json": `{
					"scripts": {"build": "vite build"},
					"devDependencies": {"vite": "5.0.0"}
				}`,
			},
			wantRule: RuleBrowserBundle,
			wantLane: vibedv1.LaneFast,
			wantTpl:  TemplateStaticNginx,
		},
		{
			name: "rule 3: wrangler.toml → workerd",
			files: map[string]string{
				"wrangler.toml": `name = "my-worker"`,
				"src/index.ts":  "export default {}",
			},
			wantRule: RuleWorkerd,
			wantLane: vibedv1.LaneFast,
			wantTpl:  TemplateWorkerd,
		},
		{
			name: "rule 3: worker.js at root → workerd",
			files: map[string]string{
				"worker.js": "addEventListener('fetch', () => {})",
			},
			wantRule: RuleWorkerd,
			wantLane: vibedv1.LaneFast,
			wantTpl:  TemplateWorkerd,
		},
		{
			name: "rule 4: spin.toml → Spin",
			files: map[string]string{
				"spin.toml": `spin_manifest_version = "1"`,
				"src/lib.rs": "fn main(){}",
			},
			wantRule: RuleSpin,
			wantLane: vibedv1.LaneFast,
			wantTpl:  TemplateSpin,
		},
		{
			name: "rule 5: package.json with deps + no SPA build",
			files: map[string]string{
				"package.json": `{
					"dependencies": {"express": "^4.18.0"},
					"scripts": {"start": "node index.js"}
				}`,
				"index.js": "require('express')",
			},
			wantRule: RuleNodeWithDeps,
			wantLane: vibedv1.LaneGeneral,
			wantTpl:  TemplateNode24,
		},
		{
			name: "rule 6: requirements.txt → Python",
			files: map[string]string{
				"requirements.txt": "flask==3.0",
				"app.py":           "from flask import Flask",
			},
			wantRule: RulePython,
			wantLane: vibedv1.LaneGeneral,
			wantTpl:  TemplatePython313,
		},
		{
			name: "rule 6: pyproject.toml → Python",
			files: map[string]string{
				"pyproject.toml": `[project]`,
				"src/app.py":     "x = 1",
			},
			wantRule: RulePython,
			wantLane: vibedv1.LaneGeneral,
			wantTpl:  TemplatePython313,
		},
		{
			name: "rule 7: go.mod → Go",
			files: map[string]string{
				"go.mod":  "module example.com/foo\ngo 1.23\n",
				"main.go": "package main",
			},
			wantRule: RuleGo,
			wantLane: vibedv1.LaneGeneral,
			wantTpl:  TemplateGo123,
		},
		{
			name: "rule 8: Dockerfile → base-al2023",
			files: map[string]string{
				"Dockerfile": "FROM scratch",
				"main":       "binary-bytes",
			},
			wantRule: RuleDockerfile,
			wantLane: vibedv1.LaneGeneral,
			wantTpl:  TemplateBaseAL2023,
		},
		{
			name: "rule 9: unknown shape → fallback",
			files: map[string]string{
				"unknown.bin": "\x00\x01\x02",
				"README":      "hi",
			},
			wantRule: RuleUnknownFallback,
			wantLane: vibedv1.LaneGeneral,
			wantTpl:  TemplateBaseAL2023,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := classify(t, tc.files)
			if d.Rule != tc.wantRule {
				t.Errorf("rule = %d, want %d (lane=%s template=%s)", d.Rule, tc.wantRule, d.Lane, d.Template)
			}
			if d.Lane != tc.wantLane {
				t.Errorf("lane = %q, want %q", d.Lane, tc.wantLane)
			}
			if d.Template != tc.wantTpl {
				t.Errorf("template = %q, want %q", d.Template, tc.wantTpl)
			}
		})
	}
}

// TestPriority makes sure higher-priority rules beat lower-priority ones
// when both signals are present. Locks in the rule ordering against
// regressions where someone re-arranges the decide() switch.
func TestPriority(t *testing.T) {
	t.Run("spin.toml + package.json → Spin wins", func(t *testing.T) {
		d := classify(t, map[string]string{
			"spin.toml":    `spin_manifest_version = "1"`,
			"package.json": `{"dependencies": {}}`,
		})
		if d.Rule != RuleSpin {
			t.Errorf("rule = %d, want %d", d.Rule, RuleSpin)
		}
	})

	t.Run("wrangler.toml + package.json → workerd wins", func(t *testing.T) {
		d := classify(t, map[string]string{
			"wrangler.toml": `name="x"`,
			"package.json":  `{"dependencies": {"express":"^4"}}`,
		})
		if d.Rule != RuleWorkerd {
			t.Errorf("rule = %d, want %d", d.Rule, RuleWorkerd)
		}
	})

	t.Run("package.json beats Dockerfile", func(t *testing.T) {
		d := classify(t, map[string]string{
			"package.json": `{"dependencies": {"express":"^4"}}`,
			"Dockerfile":   "FROM node:24",
		})
		if d.Rule != RuleNodeWithDeps {
			t.Errorf("rule = %d, want %d", d.Rule, RuleNodeWithDeps)
		}
	})

	t.Run("index.html + package.json → Node, not static", func(t *testing.T) {
		// A Node app that happens to ship an index.html template must NOT
		// be classified as static — the package.json signal wins.
		d := classify(t, map[string]string{
			"index.html":   "<html></html>",
			"package.json": `{"dependencies": {"express":"^4"}}`,
		})
		if d.Rule != RuleNodeWithDeps {
			t.Errorf("rule = %d, want %d", d.Rule, RuleNodeWithDeps)
		}
	})
}

func TestBuildAsyncFlag(t *testing.T) {
	d := classify(t, map[string]string{
		"index.html":   "<div></div>",
		"package.json": `{"scripts":{"build":"vite build"}, "devDependencies":{"vite":"5"}}`,
	})
	if !d.BuildAsync {
		t.Errorf("expected BuildAsync=true for SPA rule, got %+v", d)
	}

	d = classify(t, map[string]string{
		"index.html": "<div></div>",
	})
	if d.BuildAsync {
		t.Errorf("BuildAsync should be false for pure-static (rule 1), got true")
	}
}

func TestMalformedGzipErrors(t *testing.T) {
	_, err := Classifier{}.Classify(context.Background(), bytes.NewReader([]byte("not a gzip stream")))
	if err == nil || !strings.Contains(err.Error(), "gunzip") {
		t.Fatalf("expected gunzip error, got %v", err)
	}
}

func TestEmptyTarballFallsBack(t *testing.T) {
	// An empty (but valid) gzipped tarball has no files at all — that's
	// the unknown-shape case and should hit rule 9.
	d := classify(t, map[string]string{})
	if d.Rule != RuleUnknownFallback {
		t.Errorf("rule = %d, want %d", d.Rule, RuleUnknownFallback)
	}
}

func TestPackageJSONInSubdirIsIgnored(t *testing.T) {
	// package.json inside a subdir (e.g. a frontend/ subfolder of a Python
	// app) should not flip the decision to Node — only root markers count
	// for rule selection. The deep package.json still sets anyServerSignal
	// for rule-1 purposes, which is the intended behavior.
	d := classify(t, map[string]string{
		"requirements.txt":         "flask",
		"frontend/package.json":    `{"dependencies":{"react":"18"}}`,
		"app.py":                   "from flask import Flask",
	})
	if d.Rule != RulePython {
		t.Errorf("rule = %d, want %d (Python, because root-level package.json absent)", d.Rule, RulePython)
	}
}
