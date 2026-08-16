package runneragent

import (
	"strings"
	"testing"
)

func envMap(kvs []string) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

// The user process is untrusted code sharing the agent's container. It must
// never inherit the agent's control-API bearer token: with it, the workload can
// call /inject on itself and on any other sandbox it can reach (the token is
// issued per template, not per app) — i.e. cross-sandbox RCE.
func TestBuildEnv_DropsAgentToken(t *testing.T) {
	t.Setenv("VIBED_AGENT_TOKEN", "super-secret")
	t.Setenv("VIBED_AGENT_REQUIRE_AUTH", "true")
	t.Setenv("VIBED_RUNTIME_PROBE", "node --version")
	t.Setenv("PATH", "/usr/bin")

	got := envMap(buildEnv(nil, 8080))

	for _, leaked := range []string{"VIBED_AGENT_TOKEN", "VIBED_AGENT_REQUIRE_AUTH", "VIBED_RUNTIME_PROBE"} {
		if v, ok := got[leaked]; ok {
			t.Errorf("%s leaked to the user process (value %q)", leaked, v)
		}
	}
	// Ordinary variables the app legitimately needs still pass through.
	if got["PATH"] != "/usr/bin" {
		t.Errorf("PATH = %q, want /usr/bin", got["PATH"])
	}
	if got["PORT"] != "8080" {
		t.Errorf("PORT = %q, want 8080", got["PORT"])
	}
}

// The filter applies to the agent's OWN environment only. A deploy may still
// set a VIBED_-prefixed variable deliberately (e.g. VIBED_USER_COMMAND, which
// the template entrypoints honour), so `extra` must win.
func TestBuildEnv_DeployEnvCanSetVibedVars(t *testing.T) {
	t.Setenv("VIBED_AGENT_TOKEN", "super-secret")

	got := envMap(buildEnv(map[string]string{
		"VIBED_USER_COMMAND": "node server.js",
		"API_KEY":            "user-value",
	}, 3000))

	if got["VIBED_USER_COMMAND"] != "node server.js" {
		t.Errorf("VIBED_USER_COMMAND = %q, want the deploy's value", got["VIBED_USER_COMMAND"])
	}
	if got["API_KEY"] != "user-value" {
		t.Errorf("API_KEY = %q, want user-value", got["API_KEY"])
	}
	if _, ok := got["VIBED_AGENT_TOKEN"]; ok {
		t.Error("VIBED_AGENT_TOKEN leaked despite the filter")
	}
}
