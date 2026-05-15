// Package vibedhttp implements the /v1/* HTTP API defined by api/openapi.yaml.
//
// The generated artifacts (`types.gen.go`, `server.gen.go`) are produced from
// the spec by oapi-codegen via `make openapi-gen`. The Server type in this
// file implements ServerInterface and is the single place hand-written code
// lives — everything else flows through the spec.
//
// During milestone A2 most endpoints are deliberately stubbed with
// 501 Not Implemented. The handlers light up as later milestones land:
//   - milestone B (vibed-controller + agent) → deploy, get, list, delete,
//     redeploy, suspend, logs
//   - milestone C (classifier + templates)    → templates
//
// `/healthz`, `/readyz`, `/metrics` are wired to real handlers passed in at
// construction time so they work today.
package vibedhttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Server implements ServerInterface and bridges generated stubs to vibeD's
// internal subsystems. Inject only what each handler actually needs — the
// orchestrator handle stays out until milestone B wires deploy/list/etc.
//
// Ops handlers (health/ready/metrics) are held as fields (with the suffix
// `Handler` to avoid colliding with the ServerInterface method names) so the
// package doesn't import internal/health and tests can supply fakes.
type Server struct {
	HealthHandler  http.Handler
	ReadyHandler   http.Handler
	MetricsHandler http.Handler

	Logger *slog.Logger
}

// New constructs a Server. Nil Logger is replaced with slog.Default().
func New(health, ready, metrics http.Handler, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		HealthHandler:  health,
		ReadyHandler:   ready,
		MetricsHandler: metrics,
		Logger:         logger,
	}
}

// ---- Ops endpoints (real handlers, delegated) ----

func (s *Server) Healthz(w http.ResponseWriter, r *http.Request) {
	s.HealthHandler.ServeHTTP(w, r)
}

func (s *Server) Readyz(w http.ResponseWriter, r *http.Request) {
	s.ReadyHandler.ServeHTTP(w, r)
}

func (s *Server) Metrics(w http.ResponseWriter, r *http.Request) {
	s.MetricsHandler.ServeHTTP(w, r)
}

// ---- /v1/* endpoints: stubbed until milestones B/C land ----

func (s *Server) DeployApp(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "deploy_app", "wired in milestone B (vibed-controller)")
}

func (s *Server) ListApps(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "list_apps", "wired in milestone B (vibed-controller)")
}

func (s *Server) GetApp(w http.ResponseWriter, r *http.Request, appID AppID) {
	notImplemented(w, "get_app", "wired in milestone B (vibed-controller)")
}

func (s *Server) DeleteApp(w http.ResponseWriter, r *http.Request, appID AppID) {
	notImplemented(w, "delete_app", "wired in milestone B (vibed-controller)")
}

func (s *Server) RedeployApp(w http.ResponseWriter, r *http.Request, appID AppID) {
	notImplemented(w, "redeploy_app", "wired in milestone B (vibed-controller)")
}

func (s *Server) SuspendApp(w http.ResponseWriter, r *http.Request, appID AppID) {
	notImplemented(w, "suspend_app", "wired in milestone F1 (snapshot/restore)")
}

func (s *Server) StreamAppLogs(w http.ResponseWriter, r *http.Request, appID AppID) {
	notImplemented(w, "stream_app_logs", "wired in milestone B (vibed-controller)")
}

func (s *Server) ListTemplates(w http.ResponseWriter, r *http.Request) {
	// Pre-controller stub: return an empty list rather than 501 so MCP / CLI
	// clients can call this endpoint without special-casing milestone gating.
	// Real template enumeration lands in milestone C.
	writeJSON(w, http.StatusOK, struct {
		Items []Template `json:"items"`
	}{Items: []Template{}})
}

// ---- helpers ----

func notImplemented(w http.ResponseWriter, code, message string) {
	writeJSON(w, http.StatusNotImplemented, Error{
		Code:    code + "_not_implemented",
		Message: message,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
