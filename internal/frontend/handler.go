package frontend

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/maxkorbacher/vibed/internal/orchestrator"
)

// NewHandler creates an HTTP handler that serves the frontend and REST API.
func NewHandler(orch *orchestrator.Orchestrator) http.Handler {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/artifacts", handleArtifacts(orch))
	mux.HandleFunc("/api/targets", handleTargets(orch))

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
			// Check for /api/artifacts/{id}/logs
			parts := strings.SplitN(path, "/", 2)
			artifactID := parts[0]

			if len(parts) == 2 && parts[1] == "logs" {
				handleArtifactLogs(orch, artifactID, w, r)
				return
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
