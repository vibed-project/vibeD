// Package appspec derives how a user's source files should be run: the
// language, the entry-point file, and the run command. It is shared by the
// builder (to generate Dockerfiles) and the runner agent (to start the user
// process inside a pooled runner pod), so the two paths agree on what "run
// this app" means.
package appspec

import "strings"

// Language constants returned by DetectLanguage.
const (
	LangStatic = "static"
	LangNodeJS = "nodejs"
	LangPython = "python"
	LangGo     = "go"
	LangRust   = "rust"
)

// entrypointCandidates lists, per language, the preferred entry-point file
// names in priority order. The first match in the file map wins; if none
// match, the first candidate is used as the default.
var entrypointCandidates = map[string][]string{
	LangPython: {"app.py", "main.py", "server.py", "run.py"},
	LangNodeJS: {"index.js", "server.js", "app.js", "main.js"},
}

// DetectLanguage inspects the file map and returns the best-guess language:
// "static", "nodejs", "python", "go", or "rust".
func DetectLanguage(files map[string]string) string {
	// Collect signals in a single pass, then apply a fixed precedence. We must
	// not return inside the loop: Go map iteration order is randomized, so
	// returning on the first match makes detection nondeterministic when more
	// than one signal is present (e.g. go.mod alongside app.py).
	var hasGoMod, hasCargo, hasPkgJSON, hasPython, hasGoFile, hasHTML bool
	for name := range files {
		lower := strings.ToLower(name)
		switch {
		case lower == "go.mod":
			hasGoMod = true
		case lower == "cargo.toml":
			hasCargo = true
		case lower == "package.json":
			hasPkgJSON = true
		case lower == "requirements.txt" || lower == "main.py" || lower == "app.py":
			hasPython = true
		case strings.HasSuffix(lower, ".go"):
			hasGoFile = true
		case strings.HasSuffix(lower, ".html"):
			hasHTML = true
		}
	}
	switch {
	case hasGoMod:
		return LangGo
	case hasCargo:
		return LangRust
	case hasPkgJSON:
		return LangNodeJS
	case hasPython:
		return LangPython
	case hasGoFile:
		// A bare .go file without go.mod is still a Go app — the Dockerfile
		// handles module init + tidy automatically.
		return LangGo
	case hasHTML:
		return LangStatic
	default:
		return LangStatic
	}
}

// Entrypoint returns the best-guess entry-point file for the given language,
// picking the highest-priority candidate present in the file map and falling
// back to the language's default. Returns "" for languages that have no
// single entry-point file (static, go, rust).
func Entrypoint(language string, files map[string]string) string {
	candidates, ok := entrypointCandidates[language]
	if !ok {
		return ""
	}
	for _, candidate := range candidates {
		if _, found := files[candidate]; found {
			return candidate
		}
	}
	return candidates[0]
}

// RunCommand returns the command and args that start the app for interpreted
// languages (python, nodejs). It returns nil for compiled or static languages,
// which have no interpreter-level run command.
func RunCommand(language string, files map[string]string) []string {
	switch language {
	case LangPython:
		return []string{"python", Entrypoint(LangPython, files)}
	case LangNodeJS:
		return []string{"node", Entrypoint(LangNodeJS, files)}
	default:
		return nil
	}
}

// Interpreted reports whether the language can run on the fast path — i.e. it
// executes directly from source with no compile or container-build step.
func Interpreted(language string) bool {
	return language == LangPython || language == LangNodeJS
}
