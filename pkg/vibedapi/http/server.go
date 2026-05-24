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
	"errors"
	"log/slog"
	"net/http"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/deploy"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
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

	// Deploy backs the /v1/deploy + /v1/apps endpoints. When nil those
	// endpoints return 501 (the pre-wired A2 behavior), so the server is
	// usable for ops-only mode without a K8s connection.
	Deploy *deploy.Service

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

// maxMultipartMemory caps in-memory multipart buffering; larger parts spill
// to temp files. The deploy service enforces the real 50 MB source cap.
const maxMultipartMemory = 8 << 20 // 8 MB

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
	if s.Deploy == nil {
		notImplemented(w, "deploy_app", "deploy service not configured")
		return
	}
	owner := vibedauth.UserIDFromContext(r.Context())
	if owner == "" {
		writeJSON(w, http.StatusUnauthorized, Error{Code: "unauthenticated", Message: "no authenticated user"})
		return
	}

	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		writeJSON(w, http.StatusBadRequest, Error{Code: "bad_multipart", Message: err.Error()})
		return
	}

	// metadata: a JSON blob in the "metadata" form field.
	var meta DeployMetadata
	if raw := r.FormValue("metadata"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &meta); err != nil {
			writeJSON(w, http.StatusBadRequest, Error{Code: "bad_metadata", Message: "metadata is not valid JSON: " + err.Error()})
			return
		}
	}
	if meta.Name == "" {
		writeJSON(w, http.StatusBadRequest, Error{Code: "missing_name", Message: "metadata.name is required"})
		return
	}

	// source: the gzipped tarball file part.
	file, _, err := r.FormFile("source")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, Error{Code: "missing_source", Message: "multipart field 'source' (the gzipped tarball) is required"})
		return
	}
	defer file.Close()

	req := deploy.Request{
		Name:    meta.Name,
		Owner:   owner,
		Tarball: file,
	}
	if meta.Ttl != nil {
		req.TTL = *meta.Ttl
	}
	if meta.Runtime != nil {
		if meta.Runtime.Lane != nil {
			req.LaneOverride = vibedv1.Lane(*meta.Runtime.Lane)
		}
		if meta.Runtime.Template != nil {
			req.TemplateOverride = *meta.Runtime.Template
		}
		if meta.Runtime.Entrypoint != nil {
			req.Entrypoint = *meta.Runtime.Entrypoint
		}
	}
	if meta.Egress != nil && meta.Egress.AllowedHosts != nil {
		req.AllowedHosts = *meta.Egress.AllowedHosts
	}
	if meta.Env != nil {
		for _, e := range *meta.Env {
			ev := vibedv1.EnvVar{Name: e.Name}
			if e.Value != nil {
				ev.Value = *e.Value
			}
			req.Env = append(req.Env, ev)
		}
	}

	res, err := s.Deploy.Deploy(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, Error{Code: "deploy_failed", Message: err.Error()})
		return
	}

	resp := DeployResponse{AppId: res.AppID}
	if res.Ready {
		resp.Url = strPtr(res.URL)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	// Not ready within the budget — 202 + a status URL the client polls.
	resp.StatusUrl = strPtr("/v1/apps/" + res.AppID)
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) ListApps(w http.ResponseWriter, r *http.Request) {
	if s.Deploy == nil {
		notImplemented(w, "list_apps", "deploy service not configured")
		return
	}
	owner := vibedauth.UserIDFromContext(r.Context())
	if owner == "" {
		writeJSON(w, http.StatusUnauthorized, Error{Code: "unauthenticated", Message: "no authenticated user"})
		return
	}
	apps, err := s.Deploy.List(r.Context(), owner)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, Error{Code: "list_failed", Message: err.Error()})
		return
	}
	items := make([]App, 0, len(apps))
	for i := range apps {
		items = append(items, toAPIApp(&apps[i]))
	}
	writeJSON(w, http.StatusOK, struct {
		Items []App `json:"items"`
	}{Items: items})
}

func (s *Server) GetApp(w http.ResponseWriter, r *http.Request, appID AppID) {
	if s.Deploy == nil {
		notImplemented(w, "get_app", "deploy service not configured")
		return
	}
	owner := vibedauth.UserIDFromContext(r.Context())
	if owner == "" {
		writeJSON(w, http.StatusUnauthorized, Error{Code: "unauthenticated", Message: "no authenticated user"})
		return
	}
	app, err := s.Deploy.Get(r.Context(), owner, appID)
	if errors.Is(err, deploy.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, Error{Code: "not_found", Message: "app not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, Error{Code: "get_failed", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, toAPIApp(app))
}

func (s *Server) DeleteApp(w http.ResponseWriter, r *http.Request, appID AppID) {
	if s.Deploy == nil {
		notImplemented(w, "delete_app", "deploy service not configured")
		return
	}
	owner := vibedauth.UserIDFromContext(r.Context())
	if owner == "" {
		writeJSON(w, http.StatusUnauthorized, Error{Code: "unauthenticated", Message: "no authenticated user"})
		return
	}
	err := s.Deploy.Delete(r.Context(), owner, appID)
	if errors.Is(err, deploy.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, Error{Code: "not_found", Message: "app not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, Error{Code: "delete_failed", Message: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toAPIApp maps a VibedApp CR to the API's App shape.
func toAPIApp(app *vibedv1.VibedApp) App {
	out := App{
		AppId: app.Name,
		Phase: Phase(app.Status.Phase),
	}
	if app.Name != "" {
		out.Name = strPtr(app.Name)
	}
	if app.Spec.Owner != "" {
		out.Owner = strPtr(app.Spec.Owner)
	}
	if app.Status.URL != "" {
		out.Url = strPtr(app.Status.URL)
	}
	if app.Status.LastDeployedAt != nil {
		t := app.Status.LastDeployedAt.Time
		out.LastDeployedAt = &t
	}
	if app.Spec.Runtime.Lane != "" || app.Spec.Runtime.Template != "" {
		out.Runtime = &struct {
			Lane     *AppRuntimeLane `json:"lane,omitempty"`
			Template *string         `json:"template,omitempty"`
		}{}
		if app.Spec.Runtime.Lane != "" {
			lane := AppRuntimeLane(app.Spec.Runtime.Lane)
			out.Runtime.Lane = &lane
		}
		if app.Spec.Runtime.Template != "" {
			out.Runtime.Template = strPtr(app.Spec.Runtime.Template)
		}
	}
	return out
}

func strPtr(s string) *string { return &s }

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
