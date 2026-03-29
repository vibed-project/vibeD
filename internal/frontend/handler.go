package frontend

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
)

// writeError maps known API errors to appropriate HTTP status codes.
// Unknown errors return 500 with a generic message to avoid leaking internals.
func writeError(w http.ResponseWriter, err error, fallbackStatus int) {
	switch err.(type) {
	case *api.ErrNotFound:
		http.Error(w, "not found", http.StatusNotFound)
	case *api.ErrAlreadyExists:
		http.Error(w, err.Error(), http.StatusConflict)
	case *api.ErrInvalidInput:
		http.Error(w, err.Error(), http.StatusBadRequest)
	case *api.ErrShareLinkNotFound:
		http.Error(w, "not found", http.StatusNotFound)
	case *api.ErrPasswordRequired:
		http.Error(w, "password required", http.StatusUnauthorized)
	default:
		http.Error(w, "internal server error", fallbackStatus)
	}
}

// NewHandler creates an HTTP handler that serves the frontend and REST API.
func NewHandler(orch *orchestrator.Orchestrator, cfg *config.Config, bus *events.EventBus, m *metrics.Metrics, userStore store.UserStore) http.Handler {
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
	mux.HandleFunc("/api/whoami", handleWhoami(userStore))
	mux.HandleFunc("/api/organization", handleOrganization(cfg))
	mux.HandleFunc("/api/users", handleUsers(userStore))
	mux.HandleFunc("/api/users/", handleUserDetail(userStore))
	mux.HandleFunc("/api/departments", handleDepartments(userStore))
	mux.HandleFunc("/api/departments/", handleDepartmentDetail(userStore))

	// Share link routes (public — auth bypassed in SkipAuthPaths)
	mux.HandleFunc("/api/share/", handlePublicShareLink(orch))
	mux.HandleFunc("/api/share-links/", handleShareLinkRevoke(orch))

	// Browser-friendly share link page — serves the SPA so React renders ShareLinkPage.
	// The React app detects /share/<token> and calls /api/share/<token> as JSON.
	mux.HandleFunc("/share/", handleSPAIndex())

	// Serve static frontend files
	staticFS, _ := fs.Sub(StaticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// Wrap with request body size limit for API endpoints (64MB for deploy, default for safety)
	return limitRequestBody(mux, cfg.Limits.MaxTotalFileSize)
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
				case "share-link":
					handleArtifactShareLink(orch, artifactID, w, r)
					return
				case "share-links":
					handleArtifactShareLinks(orch, artifactID, w, r)
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

		// List artifacts with pagination
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		result, err := orch.List(r.Context(), r.URL.Query().Get("status"), offset, limit)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func handleArtifactDetail(orch *orchestrator.Orchestrator, id string, w http.ResponseWriter, r *http.Request) {
	artifact, err := orch.Status(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Sanitize sensitive fields before returning
	artifact.EnvVars = nil
	artifact.StorageRef = ""

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(artifact)
}

func handleArtifactLogs(orch *orchestrator.Orchestrator, id string, w http.ResponseWriter, r *http.Request) {
	logs, err := orch.Logs(r.Context(), id, 50)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
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
		writeError(w, err, http.StatusInternalServerError)
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
		writeError(w, err, http.StatusInternalServerError)
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
		writeError(w, err, http.StatusInternalServerError)
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
		writeError(w, err, http.StatusInternalServerError)
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
		writeError(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"artifact_id": id,
		"removed":     body.UserIDs,
		"status":      "unshared",
	})
}

func handleWhoami(userStore store.UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := vibedauth.UserIDFromContext(r.Context())
		role := vibedauth.RoleFromContext(r.Context())

		w.Header().Set("Content-Type", "application/json")

		// Try to return full user record if available
		if userStore != nil && userID != "" {
			if u, err := userStore.GetUser(r.Context(), userID); err == nil {
				json.NewEncoder(w).Encode(u)
				return
			}
		}

		// When auth is disabled there is no authenticated identity.
		// Return a synthetic guest admin so the dashboard profile and admin
		// panel are always visible in no-auth mode.
		if userID == "" {
			json.NewEncoder(w).Encode(map[string]string{
				"user_id":  "guest",
				"id":       "guest",
				"name":     "Guest",
				"role":     "admin",
				"status":   "active",
				"provider": "local",
			})
			return
		}

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

func handleUsers(userStore store.UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !vibedauth.IsAdmin(r.Context()) {
			http.Error(w, "admin access required", http.StatusForbidden)
			return
		}
		if userStore == nil {
			http.Error(w, "user store not available", http.StatusServiceUnavailable)
			return
		}

		switch r.Method {
		case http.MethodGet:
			departmentID := r.URL.Query().Get("department")
			users, err := userStore.ListUsers(r.Context(), departmentID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(users)

		case http.MethodPost:
			var body struct {
				Name         string `json:"name"`
				Email        string `json:"email"`
				Role         string `json:"role"`
				DepartmentID string `json:"department_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			if body.Name == "" {
				http.Error(w, "name is required", http.StatusBadRequest)
				return
			}
			role := body.Role
			if role == "" {
				role = "user"
			}

			// Generate API key
			keyBytes := make([]byte, 32)
			if _, err := rand.Read(keyBytes); err != nil {
				http.Error(w, "failed to generate API key", http.StatusInternalServerError)
				return
			}
			plainKey := "vibed_" + hex.EncodeToString(keyBytes)
			hash := sha256.Sum256([]byte(plainKey))
			keyHash := hex.EncodeToString(hash[:])

			now := time.Now()
			user := &api.User{
				ID:           fmt.Sprintf("u-%x", now.UnixNano()),
				Name:         body.Name,
				Email:        body.Email,
				Role:         role,
				Status:       "active",
				Provider:     "local",
				DepartmentID: body.DepartmentID,
				APIKeyHash:   keyHash,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if err := userStore.CreateUser(r.Context(), user); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(api.UserWithKey{User: *user, APIKey: plainKey})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleUserDetail(userStore store.UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userStore == nil {
			http.Error(w, "user store not available", http.StatusServiceUnavailable)
			return
		}

		userID := strings.TrimPrefix(r.URL.Path, "/api/users/")
		if userID == "" {
			http.Error(w, "user ID required", http.StatusBadRequest)
			return
		}

		callerID := vibedauth.UserIDFromContext(r.Context())
		isAdmin := vibedauth.IsAdmin(r.Context())

		switch r.Method {
		case http.MethodGet:
			if !isAdmin && callerID != userID {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			user, err := userStore.GetUser(r.Context(), userID)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(user)

		case http.MethodPatch:
			if !isAdmin {
				http.Error(w, "admin access required", http.StatusForbidden)
				return
			}
			user, err := userStore.GetUser(r.Context(), userID)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			var body struct {
				Role         *string `json:"role"`
				Status       *string `json:"status"`
				DepartmentID *string `json:"department_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			if body.Role != nil {
				user.Role = *body.Role
			}
			if body.Status != nil {
				user.Status = *body.Status
			}
			if body.DepartmentID != nil {
				user.DepartmentID = *body.DepartmentID
			}
			user.UpdatedAt = time.Now()
			if err := userStore.UpdateUser(r.Context(), user); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(user)

		case http.MethodDelete:
			if !isAdmin {
				http.Error(w, "admin access required", http.StatusForbidden)
				return
			}
			user, err := userStore.GetUser(r.Context(), userID)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			user.Status = "suspended"
			user.UpdatedAt = time.Now()
			if err := userStore.UpdateUser(r.Context(), user); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(user)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// --- Department handlers ---

func handleDepartments(userStore store.UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userStore == nil {
			http.Error(w, "user management not available", http.StatusServiceUnavailable)
			return
		}
		if !vibedauth.IsAdmin(r.Context()) {
			http.Error(w, "admin access required", http.StatusForbidden)
			return
		}

		switch r.Method {
		case http.MethodGet:
			depts, err := userStore.ListDepartments(r.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if depts == nil {
				depts = []api.Department{}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(depts)

		case http.MethodPost:
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			if body.Name == "" {
				http.Error(w, "name is required", http.StatusBadRequest)
				return
			}
			now := time.Now()
			dept := &api.Department{
				ID:        fmt.Sprintf("dept-%x", now.UnixNano()),
				Name:      body.Name,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := userStore.CreateDepartment(r.Context(), dept); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(dept)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleDepartmentDetail(userStore store.UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userStore == nil {
			http.Error(w, "user management not available", http.StatusServiceUnavailable)
			return
		}
		if !vibedauth.IsAdmin(r.Context()) {
			http.Error(w, "admin access required", http.StatusForbidden)
			return
		}

		deptID := strings.TrimPrefix(r.URL.Path, "/api/departments/")
		if deptID == "" {
			http.Error(w, "department ID required", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			dept, err := userStore.GetDepartment(r.Context(), deptID)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(dept)

		case http.MethodPatch:
			dept, err := userStore.GetDepartment(r.Context(), deptID)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			var body struct {
				Name *string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			if body.Name != nil {
				dept.Name = *body.Name
			}
			dept.UpdatedAt = time.Now()
			if err := userStore.UpdateDepartment(r.Context(), dept); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(dept)

		case http.MethodDelete:
			if err := userStore.DeleteDepartment(r.Context(), deptID); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// --- Share Link handlers ---

// handleSPAIndex serves the React SPA index.html for browser-navigated routes
// that need the frontend app (e.g. /share/<token>).
func handleSPAIndex() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		staticFS, err := fs.Sub(StaticFiles, "static")
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		f, err := staticFS.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		stat, _ := f.Stat()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", stat.ModTime(), f.(interface {
			io.ReadSeeker
		}))
	}
}

// POST /api/artifacts/{id}/share-link — create a share link
func handleArtifactShareLink(orch *orchestrator.Orchestrator, artifactID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Password  string `json:"password"`
		ExpiresIn string `json:"expires_in"` // duration string e.g. "24h", "7d"
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}

	var expiresIn time.Duration
	if body.ExpiresIn != "" {
		// Support "7d" as shorthand for 7 days
		s := body.ExpiresIn
		if strings.HasSuffix(s, "d") {
			days := strings.TrimSuffix(s, "d")
			if d, err := time.ParseDuration(days + "h"); err == nil {
				expiresIn = d * 24
			}
		} else {
			expiresIn, _ = time.ParseDuration(s)
		}
	}

	link, err := orch.CreateShareLink(r.Context(), artifactID, body.Password, expiresIn)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(link)
}

// GET /api/artifacts/{id}/share-links — list share links
func handleArtifactShareLinks(orch *orchestrator.Orchestrator, artifactID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	links, err := orch.ListShareLinks(r.Context(), artifactID)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if links == nil {
		links = []api.ShareLink{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(links)
}

// DELETE /api/share-links/{token} — revoke a share link
func handleShareLinkRevoke(orch *orchestrator.Orchestrator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, "/api/share-links/")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}

		if err := orch.RevokeShareLink(r.Context(), token); err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
	}
}

// GET/POST /api/share/{token} — public share link resolution
func handlePublicShareLink(orch *orchestrator.Orchestrator) http.HandlerFunc {
	// Per-token rate limiter: max 5 password attempts per minute per token.
	var mu sync.Mutex
	attempts := make(map[string]*tokenAttempts)

	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, "/api/share/")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}

		var password string
		if r.Method == http.MethodPost {
			// Check brute-force rate limit for password attempts
			mu.Lock()
			ta, ok := attempts[token]
			if !ok {
				ta = &tokenAttempts{}
				attempts[token] = ta
			}
			ta.cleanup()
			if ta.count() >= 5 {
				mu.Unlock()
				http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
				return
			}
			ta.record()
			mu.Unlock()

			var body struct {
				Password string `json:"password"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			password = body.Password
		}

		artifact, err := orch.ResolveShareLink(r.Context(), token, password)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}

		// Return read-only artifact view (strip sensitive fields)
		resp := map[string]interface{}{
			"name":   artifact.Name,
			"status": artifact.Status,
			"url":    artifact.URL,
			"target": artifact.Target,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// tokenAttempts tracks password attempts for a share link token (brute-force protection).
type tokenAttempts struct {
	timestamps []time.Time
}

func (t *tokenAttempts) record() {
	t.timestamps = append(t.timestamps, time.Now())
}

func (t *tokenAttempts) cleanup() {
	cutoff := time.Now().Add(-1 * time.Minute)
	n := 0
	for _, ts := range t.timestamps {
		if ts.After(cutoff) {
			t.timestamps[n] = ts
			n++
		}
	}
	t.timestamps = t.timestamps[:n]
}

func (t *tokenAttempts) count() int {
	return len(t.timestamps)
}

// limitRequestBody wraps a handler to enforce a max request body size on API endpoints.
func limitRequestBody(next http.Handler, maxBytes int) http.Handler {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024 * 1024 // 64MB default
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/mcp") {
			r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))
		}
		next.ServeHTTP(w, r)
	})
}
