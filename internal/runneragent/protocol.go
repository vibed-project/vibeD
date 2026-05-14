// Package runneragent implements the in-pod agent that runs as PID 1 inside a
// pooled runner container. It exposes a small, in-cluster-only HTTP control API
// that vibeD calls to inject a user's source files and (re)start the user
// process. The user app itself serves on a separate app port — the control API
// is never exposed through the Sandbox's public URL.
package runneragent

// Control-API paths.
const (
	PathInject  = "/inject"
	PathStatus  = "/status"
	PathLogs    = "/logs"
	PathStop    = "/stop"
	PathHealthz = "/healthz"
)

// Process states reported by the agent.
const (
	StateIdle    = "idle"    // no user process — agent up, waiting for inject
	StateRunning = "running" // user process started and not yet exited
	StateExited  = "exited"  // user process exited 0
	StateFailed  = "failed"  // user process exited non-zero, or failed to start
)

// InjectRequest is the body of POST /inject. vibeD sends the user's source
// files plus how to run them; the agent writes the files into its workdir and
// (re)starts the user process. A second inject replaces the first.
type InjectRequest struct {
	// Language is the detected language (see internal/appspec). When Command
	// is empty the agent derives the run command from Language + Files.
	Language string `json:"language"`
	// Files maps relative path → file content. Paths are sanitised by the
	// agent: absolute paths and parent-directory escapes are rejected.
	Files map[string]string `json:"files"`
	// Command, when set, is the explicit run command + args and overrides the
	// language-derived default.
	Command []string `json:"command,omitempty"`
	// Env is extra environment for the user process, merged over the agent's
	// own environment. PORT is always set to the app port.
	Env map[string]string `json:"env,omitempty"`
	// Port is the app port the user process should listen on. 0 means use the
	// agent's configured default.
	Port int `json:"port,omitempty"`
}

// StatusResponse is the body of GET /status and the response to /inject and
// /stop. It describes the current user process.
type StatusResponse struct {
	State    string   `json:"state"`
	PID      int      `json:"pid,omitempty"`
	ExitCode *int     `json:"exitCode,omitempty"`
	Command  []string `json:"command,omitempty"`
	AppPort  int      `json:"appPort,omitempty"`
	// Error carries the reason when State is StateFailed.
	Error string `json:"error,omitempty"`
}

// LogsResponse is the body of GET /logs — the most recent captured lines of
// the user process's combined stdout/stderr.
type LogsResponse struct {
	Lines []string `json:"lines"`
}

// ErrorResponse is returned for 4xx/5xx control-API responses.
type ErrorResponse struct {
	Error string `json:"error"`
}
