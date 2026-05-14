package prebaked

import (
	"encoding/json"
	"sort"
	"strings"
)

// pyVersionOps are requirement version-specifier operators, longest first so a
// prefix like "==" doesn't shadow "===".
var pyVersionOps = []string{"===", "==", ">=", "<=", "~=", "!=", ">", "<"}

// ParseRequirements extracts package names from requirements.txt content.
// analyzable is false when the file has lines that cannot be statically
// resolved to a simple package name — option lines (-r, -e, --index-url) or
// VCS / URL / local-path installs — in which case the app cannot be cleared
// for the fast path.
func ParseRequirements(content string) (deps []string, analyzable bool) {
	analyzable = true
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip inline comments.
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		// Option lines and VCS / URL / path installs can't be resolved to a
		// plain package name — mark the file un-analyzable but keep scanning.
		lower := strings.ToLower(line)
		if strings.HasPrefix(line, "-") ||
			strings.Contains(lower, "://") ||
			strings.HasPrefix(line, ".") ||
			strings.HasPrefix(line, "/") {
			analyzable = false
			continue
		}
		// Drop environment markers and extras: "flask[async] ; python_version>'3'".
		if i := strings.Index(line, ";"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if i := strings.Index(line, "["); i >= 0 {
			line = line[:i]
		}
		// Drop the version specifier.
		name := line
		for _, op := range pyVersionOps {
			if i := strings.Index(name, op); i >= 0 {
				name = name[:i]
				break
			}
		}
		if name = strings.TrimSpace(name); name != "" {
			deps = append(deps, name)
		}
	}
	return deps, analyzable
}

// ParsePackageJSON extracts runtime dependency names from package.json content.
// devDependencies are intentionally ignored — they are build-time only, and the
// fast path does not build. An error is returned only for invalid JSON.
func ParsePackageJSON(content string) ([]string, error) {
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(content), &pkg); err != nil {
		return nil, err
	}
	deps := make([]string, 0, len(pkg.Dependencies))
	for name := range pkg.Dependencies {
		deps = append(deps, name)
	}
	sort.Strings(deps)
	return deps, nil
}

// Eligible reports whether an app of the given language can run on the fast
// path: it must have an embedded runner manifest, and every dependency it
// declares must be pre-baked into that runner image. When ok is false, missing
// explains why — the un-pre-baked dependency names, or a single reason string.
func Eligible(language string, files map[string]string) (ok bool, missing []string) {
	m, found := ForLanguage(language)
	if !found {
		return false, []string{"no runner image for language " + language}
	}

	switch language {
	case "python":
		content, has := files["requirements.txt"]
		if !has {
			return true, nil // no declared dependencies
		}
		deps, analyzable := ParseRequirements(content)
		if !analyzable {
			return false, []string{"requirements.txt has lines that cannot be analysed (-r/-e, VCS, URL or path installs)"}
		}
		missing = notPrebaked(m, deps)
	case "nodejs":
		content, has := files["package.json"]
		if !has {
			return true, nil
		}
		deps, err := ParsePackageJSON(content)
		if err != nil {
			return false, []string{"package.json is not valid JSON: " + err.Error()}
		}
		missing = notPrebaked(m, deps)
	default:
		return false, []string{"fast path does not support language " + language}
	}

	sort.Strings(missing)
	return len(missing) == 0, missing
}

// notPrebaked returns the subset of deps not present in the manifest.
func notPrebaked(m *Manifest, deps []string) []string {
	var missing []string
	for _, d := range deps {
		if !m.Has(d) {
			missing = append(missing, d)
		}
	}
	return missing
}
