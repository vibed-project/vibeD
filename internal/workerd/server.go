package workerd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/vibed-project/vibeD/internal/runneragent"
)

// DeployRequest is the body of POST /deploy.
type DeployRequest struct {
	AppID     string `json:"app_id"`
	SourceURL string `json:"source_url"`
	// Entrypoint names the worker module inside the tarball. Empty falls back
	// to the well-known candidates (worker.js, src/index.js, …).
	Entrypoint string `json:"entrypoint,omitempty"`
}

// DeployResponse reports the socket port the app's worker now listens on.
type DeployResponse struct {
	Port int `json:"port"`
}

// entryCandidates are tried in order when no explicit entrypoint is given.
// workerd runs ES modules; .ts would need a build step we don't do here.
var entryCandidates = []string{"worker.js", "src/index.js", "index.js", "src/worker.js"}

// fetcher abstracts tarball download+extract so tests can stub it.
type fetcher interface {
	Fetch(ctx context.Context, sourceURL, dir string) error
}

// Handler is the loader's HTTP control API (POST /deploy, DELETE /apps/{id},
// GET /healthz). Token, when set, is required as a bearer credential.
type Handler struct {
	Loader  *Loader
	Token   string
	Fetcher fetcher
}

// NewHandler wires a Handler with the default runneragent SourceFetcher.
func NewHandler(l *Loader, token string) *Handler {
	return &Handler{Loader: l, Token: token, Fetcher: runneragent.NewSourceFetcher()}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/deploy", h.auth(h.handleDeploy))
	mux.HandleFunc("/apps/", h.auth(h.handleApp))
	return mux
}

func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.Token != "" && r.Header.Get("Authorization") != "Bearer "+h.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (h *Handler) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	var req DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.AppID == "" || req.SourceURL == "" {
		http.Error(w, "app_id and source_url are required", http.StatusBadRequest)
		return
	}

	script, err := h.fetchScript(r.Context(), req.SourceURL, req.Entrypoint)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	port, err := h.Loader.Put(req.AppID, script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, DeployResponse{Port: port})
}

func (h *Handler) handleApp(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/apps/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "use DELETE", http.StatusMethodNotAllowed)
		return
	}
	if err := h.Loader.Remove(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fetchScript downloads + extracts the tarball to a temp dir and returns the
// entry module's contents.
func (h *Handler) fetchScript(ctx context.Context, sourceURL, entrypoint string) (string, error) {
	dir, err := os.MkdirTemp("", "workerd-src-*")
	if err != nil {
		return "", fmt.Errorf("staging dir: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := h.Fetcher.Fetch(ctx, sourceURL, dir); err != nil {
		return "", err
	}

	candidates := entryCandidates
	if entrypoint != "" {
		candidates = append([]string{entrypoint}, entryCandidates...)
	}
	for _, c := range candidates {
		// Guard against path escapes from a hostile entrypoint value.
		clean := filepath.Clean(c)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, clean)); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("no worker entry module found (looked for %s)", strings.Join(candidates, ", "))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
