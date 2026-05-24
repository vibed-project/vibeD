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
	PathInfo    = "/info"
)

// AgentContract is the base-image contract version this agent implements.
// Reported by GET /info so vibeD can reject images built against an
// incompatible contract. Bump when the contract (env vars, paths, ports)
// changes incompatibly.
const AgentContract = "1"

// InfoResponse is the body of GET /info — static metadata about the image the
// agent is baked into, used by vibeD to validate bring-your-own base images:
// it confirms the image declares the expected language and that the language
// runtime is actually present. Unauthenticated (read-only metadata, in-cluster
// control port only), like /healthz.
type InfoResponse struct {
	// Language is the image's declared template language (from the
	// VIBED_TEMPLATE_LANGUAGE env), e.g. "node", "python", "go", "static".
	Language string `json:"language"`
	// AgentContract is the contract version the agent implements (AgentContract).
	AgentContract string `json:"agentContract"`
	// RuntimeProbe is the command (VIBED_RUNTIME_PROBE) the agent ran to prove
	// the language runtime exists, e.g. "node --version". Empty if the image
	// declared none — which BYO validation treats as a contract failure.
	RuntimeProbe string `json:"runtimeProbe,omitempty"`
	// RuntimeProbeOK is true when the probe command ran and exited 0.
	RuntimeProbeOK bool `json:"runtimeProbeOK"`
	// RuntimeProbeOutput is the probe's combined stdout/stderr, trimmed (e.g.
	// "v24.1.0"). Surfaced so validation failures are debuggable.
	RuntimeProbeOutput string `json:"runtimeProbeOutput,omitempty"`
}

// Process states reported by the agent.
const (
	StateIdle    = "idle"    // no user process — agent up, waiting for inject
	StateRunning = "running" // user process started and not yet exited
	StateExited  = "exited"  // user process exited 0
	StateFailed  = "failed"  // user process exited non-zero, or failed to start
)

// InjectRequest is the body of POST /inject. vibeD sends the user's source
// — as inline Files or as a SourceURL pointing at a gzipped tarball — plus
// how to run them; the agent writes the source into its workdir and
// (re)starts the user process. A second inject replaces the first.
//
// Exactly one of Files or SourceURL must be set. SourceURL is the
// refactor.md §5.4 contract (S3 pre-signed URL); Files is the legacy inline
// path that vibeD's in-process pool code still uses pre-milestone-C.
type InjectRequest struct {
	// Language is the detected language (see internal/appspec). When Command
	// is empty the agent derives the run command from Language + Files.
	Language string `json:"language"`
	// Files maps relative path → file content. Paths are sanitised by the
	// agent: absolute paths and parent-directory escapes are rejected.
	Files map[string]string `json:"files,omitempty"`
	// SourceURL points at a gzipped tarball the agent fetches and extracts
	// into the workdir. Use this for any source that exceeds a comfortable
	// JSON payload (~ a few MB) — the agent enforces a 50 MB tarball cap to
	// match the refactor.md §5.1 limit.
	SourceURL string `json:"source_url,omitempty"`
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
