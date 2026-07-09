// Package vibedhttp implements the /v1/* HTTP API defined by api/openapi.yaml.
//
// The generated artifacts (`types.gen.go`, `server.gen.go`) are produced from
// the spec by oapi-codegen via `make openapi-gen`. The Server type in this
// file implements ServerInterface and is the single place hand-written code
// lives — everything else flows through the spec.
//
// Live endpoints: deploy, get, list, delete, redeploy, and logs (SSE) run
// against the VibedApp deploy service; templates returns an empty list pending
// the classifier enumeration; ops (`/healthz`, `/readyz`, `/metrics`) delegate
// to handlers passed in at construction. Suspend remains 501 until the
// snapshot/restore milestone (F1) lands. When the deploy service isn't
// configured the deploy-backed endpoints return 501 so vibeD still boots in
// ops-only mode.
package vibedhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

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

	// MaxConcurrentLogStreamsPerUser caps simultaneous /v1/logs SSE
	// connections per authenticated user. 0 disables the cap (legacy
	// behavior). Hard-coded gate against a trivial DoS where one user opens
	// many streams to exhaust controller memory.
	MaxConcurrentLogStreamsPerUser int

	logStreamMu sync.Mutex
	logStreams  map[string]int // userID -> current open count

	Logger *slog.Logger
}

// acquireLogStream reserves one stream slot for user. Returns false (over
// cap) when MaxConcurrentLogStreamsPerUser > 0 and the user is already at it.
// Each acquire must be paired with releaseLogStream.
func (s *Server) acquireLogStream(user string) bool {
	if s.MaxConcurrentLogStreamsPerUser <= 0 {
		return true
	}
	s.logStreamMu.Lock()
	defer s.logStreamMu.Unlock()
	if s.logStreams == nil {
		s.logStreams = map[string]int{}
	}
	if s.logStreams[user] >= s.MaxConcurrentLogStreamsPerUser {
		return false
	}
	s.logStreams[user]++
	return true
}

func (s *Server) releaseLogStream(user string) {
	if s.MaxConcurrentLogStreamsPerUser <= 0 {
		return
	}
	s.logStreamMu.Lock()
	defer s.logStreamMu.Unlock()
	if s.logStreams[user] > 0 {
		s.logStreams[user]--
	}
	if s.logStreams[user] == 0 {
		delete(s.logStreams, user)
	}
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
	// forcedName "" → take the name from metadata (a first-time deploy).
	s.runDeploy(w, r, owner, "")
}

// runDeploy parses a multipart deploy (source tarball + optional metadata) and
// runs it through the deploy service, writing the 200/202/4xx response. When
// forcedName is set (a redeploy of an existing app) it overrides metadata.name.
func (s *Server) runDeploy(w http.ResponseWriter, r *http.Request, owner, forcedName string) {
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
	name := forcedName
	if name == "" {
		name = meta.Name
	}
	if name == "" {
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
		Name:    name,
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
		var pe interface{ PolicyDenied() bool }
		if errors.As(err, &pe) {
			writeJSON(w, http.StatusForbidden, Error{Code: "policy_denied", Message: err.Error()})
			return
		}
		var qe interface{ QuotaExceeded() bool }
		if errors.As(err, &qe) {
			writeJSON(w, http.StatusTooManyRequests, Error{Code: "quota_exceeded", Message: err.Error()})
			return
		}
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
	if s.Deploy == nil {
		notImplemented(w, "redeploy_app", "deploy service not configured")
		return
	}
	owner := vibedauth.UserIDFromContext(r.Context())
	if owner == "" {
		writeJSON(w, http.StatusUnauthorized, Error{Code: "unauthenticated", Message: "no authenticated user"})
		return
	}
	// Confirm the caller owns an existing app before mutating it — a redeploy
	// reuses the named CR, so without this an unrelated caller could clobber it.
	if _, err := s.Deploy.Get(r.Context(), owner, appID); err != nil {
		if errors.Is(err, deploy.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, Error{Code: "not_found", Message: "app not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, Error{Code: "get_failed", Message: err.Error()})
		return
	}
	s.runDeploy(w, r, owner, appID)
}

func (s *Server) SuspendApp(w http.ResponseWriter, r *http.Request, appID AppID) {
	s.setSuspended(w, r, appID, true)
}

func (s *Server) ResumeApp(w http.ResponseWriter, r *http.Request, appID AppID) {
	s.setSuspended(w, r, appID, false)
}

// setSuspended backs the suspend/resume endpoints: it toggles the app's
// desired state (ownership-checked) and returns 202 + the App. The controller
// reconciles the actual release/re-claim asynchronously.
func (s *Server) setSuspended(w http.ResponseWriter, r *http.Request, appID AppID, suspended bool) {
	if s.Deploy == nil {
		notImplemented(w, "suspend_app", "deploy service not configured")
		return
	}
	owner := vibedauth.UserIDFromContext(r.Context())
	if owner == "" {
		writeJSON(w, http.StatusUnauthorized, Error{Code: "unauthenticated", Message: "no authenticated user"})
		return
	}
	app, err := s.Deploy.SetSuspended(r.Context(), owner, appID, suspended)
	if err != nil {
		if errors.Is(err, deploy.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, Error{Code: "not_found", Message: "app not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, Error{Code: "suspend_failed", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIApp(app))
}

func (s *Server) StreamAppLogs(w http.ResponseWriter, r *http.Request, appID AppID) {
	if s.Deploy == nil {
		notImplemented(w, "stream_app_logs", "deploy service not configured")
		return
	}
	owner := vibedauth.UserIDFromContext(r.Context())
	if owner == "" {
		writeJSON(w, http.StatusUnauthorized, Error{Code: "unauthenticated", Message: "no authenticated user"})
		return
	}
	// Resolve ownership/existence up front so we can return a clean 404 before
	// switching the response into an SSE stream.
	if _, err := s.Deploy.Get(r.Context(), owner, appID); err != nil {
		if errors.Is(err, deploy.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, Error{Code: "not_found", Message: "app not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, Error{Code: "get_failed", Message: err.Error()})
		return
	}
	// Per-user concurrent-stream cap: blocks the trivial DoS where one
	// caller opens dozens of streams to exhaust controller memory (each
	// stream allocates a 1 MB scanner buffer).
	if !s.acquireLogStream(owner) {
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusTooManyRequests, Error{
			Code:    "too_many_log_streams",
			Message: fmt.Sprintf("max %d concurrent log streams per user", s.MaxConcurrentLogStreamsPerUser),
		})
		return
	}
	defer s.releaseLogStream(owner)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, Error{Code: "no_streaming", Message: "response writer does not support streaming"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Default to a terminating snapshot of the recent tail so a plain GET
	// completes (the dashboard polls it); `?follow=true` keeps the stream open
	// for a live tail.
	follow := r.URL.Query().Get("follow") == "true"
	err := s.Deploy.StreamLogs(r.Context(), owner, appID, follow, 200, func(line string) error {
		if _, werr := fmt.Fprintf(w, "data: %s\n\n", line); werr != nil {
			return werr
		}
		flusher.Flush()
		return nil
	})
	// A cancelled context is the client disconnecting — not an error to report.
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", strings.ReplaceAll(err.Error(), "\n", " "))
		flusher.Flush()
	}
}

func (s *Server) GetAppVersions(w http.ResponseWriter, r *http.Request, appID AppID) {
	if s.Deploy == nil {
		notImplemented(w, "get_app_versions", "deploy service not configured")
		return
	}
	owner := vibedauth.UserIDFromContext(r.Context())
	if owner == "" {
		writeJSON(w, http.StatusUnauthorized, Error{Code: "unauthenticated", Message: "no authenticated user"})
		return
	}
	recs, err := s.Deploy.Versions(r.Context(), owner, appID)
	if err != nil {
		if errors.Is(err, deploy.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, Error{Code: "not_found", Message: "app not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, Error{Code: "versions_failed", Message: err.Error()})
		return
	}
	items := make([]Version, 0, len(recs))
	for _, rc := range recs {
		ts, _ := time.Parse(time.RFC3339, rc.Timestamp)
		v := Version{
			Version:    rc.Version,
			Timestamp:  ts,
			Template:   strPtr(rc.Template),
			Lane:       strPtr(rc.Lane),
			SourceHash: strPtr(rc.SourceHash),
		}
		if rc.RolledBackFrom != 0 {
			rf := rc.RolledBackFrom
			v.RolledBackFrom = &rf
		}
		items = append(items, v)
	}
	writeJSON(w, http.StatusOK, struct {
		Items []Version `json:"items"`
	}{Items: items})
}

func (s *Server) RollbackApp(w http.ResponseWriter, r *http.Request, appID AppID) {
	if s.Deploy == nil {
		notImplemented(w, "rollback_app", "deploy service not configured")
		return
	}
	owner := vibedauth.UserIDFromContext(r.Context())
	if owner == "" {
		writeJSON(w, http.StatusUnauthorized, Error{Code: "unauthenticated", Message: "no authenticated user"})
		return
	}
	var body RollbackAppJSONRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, Error{Code: "bad_body", Message: `body must be {"version": <int>}`})
		return
	}
	res, err := s.Deploy.Rollback(r.Context(), owner, appID, body.Version)
	if err != nil {
		if errors.Is(err, deploy.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, Error{Code: "not_found", Message: "app not found"})
			return
		}
		// e.g. "version N is not available for rollback".
		writeJSON(w, http.StatusBadRequest, Error{Code: "rollback_failed", Message: err.Error()})
		return
	}
	resp := DeployResponse{AppId: res.AppID}
	if res.Ready {
		resp.Url = strPtr(res.URL)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.StatusUrl = strPtr("/v1/apps/" + res.AppID)
	writeJSON(w, http.StatusAccepted, resp)
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
