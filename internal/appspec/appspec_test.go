package appspec

import (
	"reflect"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"go.mod wins", map[string]string{"go.mod": "", "app.py": ""}, LangGo},
		{"cargo.toml", map[string]string{"Cargo.toml": ""}, LangRust},
		{"package.json", map[string]string{"package.json": ""}, LangNodeJS},
		{"requirements.txt", map[string]string{"requirements.txt": ""}, LangPython},
		{"main.py", map[string]string{"main.py": ""}, LangPython},
		{"bare .go file", map[string]string{"server.go": ""}, LangGo},
		{"html only", map[string]string{"index.html": ""}, LangStatic},
		{"empty", map[string]string{}, LangStatic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectLanguage(tt.files); got != tt.want {
				t.Errorf("DetectLanguage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEntrypoint(t *testing.T) {
	tests := []struct {
		name  string
		lang  string
		files map[string]string
		want  string
	}{
		{"python prefers app.py", LangPython, map[string]string{"app.py": "", "main.py": ""}, "app.py"},
		{"python falls to main.py", LangPython, map[string]string{"main.py": ""}, "main.py"},
		{"python default when none match", LangPython, map[string]string{"other.py": ""}, "app.py"},
		{"node prefers index.js", LangNodeJS, map[string]string{"index.js": "", "server.js": ""}, "index.js"},
		{"node falls to server.js", LangNodeJS, map[string]string{"server.js": ""}, "server.js"},
		{"go has no entrypoint file", LangGo, map[string]string{"main.go": ""}, ""},
		{"static has no entrypoint file", LangStatic, map[string]string{"index.html": ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Entrypoint(tt.lang, tt.files); got != tt.want {
				t.Errorf("Entrypoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunCommand(t *testing.T) {
	tests := []struct {
		name  string
		lang  string
		files map[string]string
		want  []string
	}{
		{"python", LangPython, map[string]string{"main.py": ""}, []string{"python", "main.py"}},
		{"node", LangNodeJS, map[string]string{"server.js": ""}, []string{"node", "server.js"}},
		{"go has no run command", LangGo, map[string]string{"main.go": ""}, nil},
		{"static has no run command", LangStatic, map[string]string{"index.html": ""}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RunCommand(tt.lang, tt.files); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RunCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInterpreted(t *testing.T) {
	for lang, want := range map[string]bool{
		LangPython: true,
		LangNodeJS: true,
		LangGo:     false,
		LangRust:   false,
		LangStatic: false,
	} {
		if got := Interpreted(lang); got != want {
			t.Errorf("Interpreted(%q) = %v, want %v", lang, got, want)
		}
	}
}
