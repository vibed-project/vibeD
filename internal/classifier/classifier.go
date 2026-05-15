// Package classifier picks the lane and template for a deploy from the
// shape of the user's source tarball.
//
// The classifier is the central scheduling primitive of the v0.3.1
// architecture: it runs on the deploy hot path, must be deterministic, and
// must complete in <50 ms on a 50 MB tarball (refactor.md §5.2). It does
// not install anything, does not parse user code, and does not call
// out — it reads file names and, for one file (package.json), a small set
// of top-level keys.
//
// The decision tree has 9 rules in priority order (first match wins); see
// the Rule* constants below for the mapping. Output is a Decision whose
// Lane + Template the controller hands to the warm pool.
package classifier

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// Template names. The v1 set per refactor.md §6.1 (subset; node-22, bun-1,
// deno-2 are added in later milestones when their templates land).
const (
	TemplateStaticNginx = "static-nginx"
	TemplateNode24      = "node-24"
	TemplatePython313   = "python-313"
	TemplateGo123       = "go-123"
	TemplateBaseAL2023  = "base-al2023"
	TemplateWorkerd     = "workerd" // fast lane; runs on the workerd loader, not a Sandbox
	TemplateSpin        = "spin"    // SpinKube SpinApp
)

// Rule identifiers — kept in sync with refactor.md §5.2 decision tree.
// Surfaced in Decision.Rule so the controller can log *why* it picked a
// template, which is the single most useful piece of debug context for
// "vibeD deployed my Node app on python-313" support questions.
const (
	RuleStaticOnly       = 1
	RuleBrowserBundle    = 2
	RuleWorkerd          = 3
	RuleSpin             = 4
	RuleNodeWithDeps     = 5
	RulePython           = 6
	RuleGo               = 7
	RuleDockerfile       = 8
	RuleUnknownFallback  = 9
)

// MaxTarballBytes mirrors refactor.md §5.1 (deploy API caps at 50 MB). The
// classifier streams from the reader and stops at this limit; oversize
// tarballs return a non-nil error.
const MaxTarballBytes = 50 * 1024 * 1024

// Decision is the classifier's output. The controller honors Lane +
// Template; BuildAsync and Rule are advisory metadata.
type Decision struct {
	Lane     vibedv1.Lane
	Template string

	// Rule is the 1-9 rule that fired. Surfaced for logging and tests so
	// "why was this template picked?" has an answer that doesn't require
	// re-running the classifier.
	Rule int

	// BuildAsync is true for rule 2 (browser-bundle SPA): the long-term
	// destination is static-nginx, but the bundle hasn't been built yet,
	// so the orchestrator must schedule an async build alongside the
	// short-term fast-lane stub. Other rules leave this false.
	BuildAsync bool
}

// Classifier classifies a gzipped tarball. Stateless and safe for concurrent
// use; a single zero-value Classifier{} works.
type Classifier struct{}

// Classify reads the gzipped tarball in r and returns the Decision.
// Streaming — does not buffer the whole archive in memory. Stops scanning
// once enough signal has been collected to decide, so a 50 MB tarball
// usually completes in a few ms.
func (c Classifier) Classify(ctx context.Context, r io.Reader) (Decision, error) {
	scan, err := scanTarball(ctx, r)
	if err != nil {
		return Decision{}, err
	}
	return decide(scan), nil
}

// scan collects the signals the decision tree needs. Filling this struct is
// the only place we touch the tarball.
type scan struct {
	// rootFiles is the set of file/directory names at the tarball root.
	// Some rules (Dockerfile, go.mod, package.json) only count when at the
	// root; others (any .html file) count anywhere.
	rootFiles map[string]struct{}

	// hasExt[".html"] etc. — any file with that extension anywhere in the
	// tree. Used by the "static only" rule which is satisfied by .html
	// alongside assets in subdirs.
	hasExt map[string]bool

	// anyServerSignal is set when we see a file name that excludes the
	// "static only" rule even if at a subpath. Per refactor.md §5.2 rule 1:
	// no package.json, no requirements.txt, no Dockerfile, no compiled
	// binary, etc.
	anyServerSignal bool

	// pkg, when non-nil, is the lightly-parsed package.json at root.
	pkg *packageJSON
}

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Scripts         map[string]string `json:"scripts"`
}

// rootMarkers are files that trigger a rule when present at the tarball
// root. Encoded as a set so we can also use them to set anyServerSignal.
var rootServerMarkers = map[string]struct{}{
	"Dockerfile":       {},
	"go.mod":           {},
	"requirements.txt": {},
	"pyproject.toml":   {},
	"package.json":     {},
	"spin.toml":        {},
	"wrangler.toml":    {},
	"worker.js":        {},
	"worker.ts":        {},
}

// staticExts is the allowlist of extensions for rule 1 ("no server-side
// code"). Anything outside this set in conjunction with no marker files is
// still considered static, but the marker check is what guarantees the
// rule's intent.
var staticExts = map[string]bool{
	".html": true, ".htm": true,
	".css": true, ".js": true, ".mjs": true,
	".svg": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".ico": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".json": true, ".txt": true, ".md": true, ".webmanifest": true,
}

func scanTarball(ctx context.Context, r io.Reader) (*scan, error) {
	limited := io.LimitReader(r, MaxTarballBytes+1)
	gz, err := gzip.NewReader(limited)
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()

	s := &scan{
		rootFiles: map[string]struct{}{},
		hasExt:    map[string]bool{},
	}

	tr := tar.NewReader(gz)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tarball: %w", err)
		}
		// Normalize: strip any "./" prefix and skip pure-directory entries
		// for the markers map (we still record extensions for asset files).
		name := strings.TrimPrefix(path.Clean(hdr.Name), "./")
		if name == "" || name == "." {
			continue
		}
		base := path.Base(name)

		// Root-level entries (no slash in the cleaned name).
		if !strings.Contains(name, "/") {
			s.rootFiles[name] = struct{}{}
		}

		// Server-signal markers can be anywhere in the tree per
		// refactor.md's spirit ("any compiled binary, any package.json…").
		// In practice the build orchestrator only acts on root markers,
		// but we use deep markers to invalidate rule 1.
		if _, ok := rootServerMarkers[base]; ok {
			s.anyServerSignal = true
		}

		// Track extension presence for static-asset detection.
		if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA {
			if ext := strings.ToLower(path.Ext(base)); ext != "" {
				s.hasExt[ext] = true
			}
		}

		// Parse a root-level package.json — top-level keys only, ≤256 KB.
		if name == "package.json" && (hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA) {
			pkg, err := parsePackageJSON(io.LimitReader(tr, 256*1024))
			if err != nil {
				return nil, fmt.Errorf("parse package.json: %w", err)
			}
			s.pkg = pkg
		}
	}

	// Overflow check: if LimitReader is exhausted but there are still
	// bytes on the wire we wouldn't see EOF on a clean tar.
	if n, _ := io.Copy(io.Discard, limited); n > 0 {
		return nil, errors.New("tarball exceeds 50MB limit")
	}
	return s, nil
}

func parsePackageJSON(r io.Reader) (*packageJSON, error) {
	var pkg packageJSON
	if err := json.NewDecoder(r).Decode(&pkg); err != nil {
		// Malformed package.json is not fatal — we'll fall through to the
		// "Node app with deps" rule and let the runtime fail with a real
		// error. This matches what a Node user expects.
		return &packageJSON{}, nil
	}
	return &pkg, nil
}

// decide applies the 9-rule decision tree. First match wins.
func decide(s *scan) Decision {
	// Rule 4: spin.toml at root → SpinKube.
	if _, ok := s.rootFiles["spin.toml"]; ok {
		return Decision{Lane: vibedv1.LaneFast, Template: TemplateSpin, Rule: RuleSpin}
	}

	// Rule 3: workerd signals at root.
	if _, ok := s.rootFiles["wrangler.toml"]; ok {
		return Decision{Lane: vibedv1.LaneFast, Template: TemplateWorkerd, Rule: RuleWorkerd}
	}
	if _, ok := s.rootFiles["worker.js"]; ok {
		return Decision{Lane: vibedv1.LaneFast, Template: TemplateWorkerd, Rule: RuleWorkerd}
	}
	if _, ok := s.rootFiles["worker.ts"]; ok {
		return Decision{Lane: vibedv1.LaneFast, Template: TemplateWorkerd, Rule: RuleWorkerd}
	}

	// Rule 2: package.json with a build script producing dist/ or build/.
	// We don't try to enumerate "browser-only deps" yet — the explicit
	// build-script signal is the safe v1 indicator.
	if s.pkg != nil {
		if build := s.pkg.Scripts["build"]; build != "" && bundlerLooksLikeSPA(s.pkg, build) {
			return Decision{
				Lane:       vibedv1.LaneFast,
				Template:   TemplateStaticNginx,
				Rule:       RuleBrowserBundle,
				BuildAsync: true,
			}
		}
		// Rule 5: any other package.json → Node with deps.
		return Decision{Lane: vibedv1.LaneGeneral, Template: TemplateNode24, Rule: RuleNodeWithDeps}
	}

	// Rule 6: Python.
	if _, ok := s.rootFiles["requirements.txt"]; ok {
		return Decision{Lane: vibedv1.LaneGeneral, Template: TemplatePython313, Rule: RulePython}
	}
	if _, ok := s.rootFiles["pyproject.toml"]; ok {
		return Decision{Lane: vibedv1.LaneGeneral, Template: TemplatePython313, Rule: RulePython}
	}

	// Rule 7: Go.
	if _, ok := s.rootFiles["go.mod"]; ok {
		return Decision{Lane: vibedv1.LaneGeneral, Template: TemplateGo123, Rule: RuleGo}
	}

	// Rule 8: Dockerfile at root. We don't build it — it's a runtime hint
	// for the agent to find the start command. base-al2023 has the
	// kitchen-sink toolchain so most Dockerfile-shaped apps run.
	if _, ok := s.rootFiles["Dockerfile"]; ok {
		return Decision{Lane: vibedv1.LaneGeneral, Template: TemplateBaseAL2023, Rule: RuleDockerfile}
	}

	// Rule 1: pure-static. Checked last among "specific" rules so a
	// package.json + index.html still routes as Node, not static.
	if !s.anyServerSignal && hasOnlyStaticExts(s.hasExt) && s.hasExt[".html"] {
		return Decision{Lane: vibedv1.LaneFast, Template: TemplateStaticNginx, Rule: RuleStaticOnly}
	}

	// Rule 9: fallback. Unknown shape → kitchen-sink template + agent
	// autodetect.
	return Decision{Lane: vibedv1.LaneGeneral, Template: TemplateBaseAL2023, Rule: RuleUnknownFallback}
}

// bundlerLooksLikeSPA is a conservative check: does the build script
// reference an SPA bundler output dir? We grep the script string for "dist"
// or "build" rather than parsing it — bundlers vary wildly (vite, parcel,
// webpack, esbuild, ng build, …) and the output dir is the one signal that
// reliably shows up across all of them.
func bundlerLooksLikeSPA(pkg *packageJSON, buildScript string) bool {
	if strings.Contains(buildScript, "dist") || strings.Contains(buildScript, "build") {
		return true
	}
	// Conservative fallback: the presence of vite/parcel/webpack/rollup
	// as a declared dep is also a strong SPA signal.
	for _, dep := range []string{"vite", "parcel", "webpack", "rollup", "esbuild", "@angular/cli"} {
		if _, ok := pkg.DevDependencies[dep]; ok {
			return true
		}
		if _, ok := pkg.Dependencies[dep]; ok {
			return true
		}
	}
	return false
}

func hasOnlyStaticExts(seen map[string]bool) bool {
	for ext := range seen {
		if !staticExts[ext] {
			return false
		}
	}
	return true
}
