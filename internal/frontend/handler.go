package frontend

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/orchestrator"
)

// NewHandler creates an HTTP handler that serves the frontend and REST API.
func NewHandler(orch *orchestrator.Orchestrator, cfg *config.Config, bus *events.EventBus, m *metrics.Metrics) http.Handler {
	mux := http.NewServeMux()

	// API documentation (Swagger UI)
	mux.Handle("/api/docs", http.RedirectHandler("/api/docs/", http.StatusMovedPermanently))
	mux.Handle("/api/docs/", http.StripPrefix("/api/docs", swaggerUIHandler()))

	// SSE event stream
	mux.HandleFunc("/api/events", handleSSE(bus, m))

	// API routes
	mux.HandleFunc("/api/artifacts", handleArtifacts(orch))
	mux.HandleFunc("/api/artifacts/", handleArtifacts(orch))
	mux.HandleFunc("/api/targets", handleTargets(orch))
	mux.HandleFunc("/api/whoami", handleWhoami())
	mux.HandleFunc("/api/organization", handleOrganization(cfg))

	// Serve static frontend files
	staticFS, _ := fs.Sub(StaticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	return mux
}

func handleArtifacts(orch *orchestrator.Orchestrator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Handle /api/artifacts/{id} paths
		path := strings.TrimPrefix(r.URL.Path, "/api/artifacts")
		path = strings.TrimPrefix(path, "/")

		if path != "" {
			parts := strings.SplitN(path, "/", 2)
			artifactID := parts[0]

			if len(parts) == 2 {
				switch parts[1] {
				case "logs":
					handleArtifactLogs(orch, artifactID, w, r)
					return
				case "versions":
					handleArtifactVersions(orch, artifactID, w, r)
					return
				case "rollback":
					handleArtifactRollback(orch, artifactID, w, r)
					return
				case "share":
					handleArtifactShare(orch, artifactID, w, r)
					return
				case "unshare":
					handleArtifactUnshare(orch, artifactID, w, r)
					return
				}
			}

			if r.Method == http.MethodDelete {
				handleArtifactDelete(orch, artifactID, w, r)
				return
			}

			handleArtifactDetail(orch, artifactID, w, r)
			return
		}

		// List all artifacts
		artifacts, err := orch.List(r.Context(), r.URL.Query().Get("status"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(artifacts)
	}
}

func handleArtifactDetail(orch *orchestrator.Orchestrator, id string, w http.ResponseWriter, r *http.Request) {
	artifact, err := orch.Status(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(artifact)
}

func handleArtifactLogs(orch *orchestrator.Orchestrator, id string, w http.ResponseWriter, r *http.Request) {
	logs, err := orch.Logs(r.Context(), id, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"artifact_id": id,
		"logs":        logs,
	})
}

func handleArtifactDelete(orch *orchestrator.Orchestrator, id string, w http.ResponseWriter, r *http.Request) {
	if err := orch.Delete(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "deleted",
		"id":     id,
	})
}

func handleTargets(orch *orchestrator.Orchestrator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		targets := orch.ListTargets()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(targets)
	}
}

func handleArtifactVersions(orch *orchestrator.Orchestrator, id string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	versions, err := orch.ListVersions(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"artifact_id": id,
		"versions":    versions,
	})
}

func handleArtifactRollback(orch *orchestrator.Orchestrator, id string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Version <= 0 {
		http.Error(w, "version must be a positive integer", http.StatusBadRequest)
		return
	}

	result, err := orch.Rollback(r.Context(), id, body.Version)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleArtifactShare(orch *orchestrator.Orchestrator, id string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(body.UserIDs) == 0 {
		http.Error(w, "user_ids is required", http.StatusBadRequest)
		return
	}

	if err := orch.ShareArtifact(r.Context(), id, body.UserIDs); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"artifact_id": id,
		"shared_with": body.UserIDs,
		"status":      "shared",
	})
}

func handleArtifactUnshare(orch *orchestrator.Orchestrator, id string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(body.UserIDs) == 0 {
		http.Error(w, "user_ids is required", http.StatusBadRequest)
		return
	}

	if err := orch.UnshareArtifact(r.Context(), id, body.UserIDs); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"artifact_id": id,
		"removed":     body.UserIDs,
		"status":      "unshared",
	})
}

func handleWhoami() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := vibedauth.UserIDFromContext(r.Context())
		role := vibedauth.RoleFromContext(r.Context())

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"user_id": userID,
			"role":    role,
		})
	}
}

func handleOrganization(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"name": cfg.Organization.Name,
		})
	}
}

