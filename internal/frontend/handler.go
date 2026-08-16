package frontend

import (
        "context"
        "crypto/rand"
        "crypto/sha256"
        "encoding/hex"
        "encoding/json"
        "errors"
        "fmt"
        "io"
        "io/fs"
        "net/http"
        "strconv"
        "strings"
        "time"

        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

        vibedauth "github.com/vibed-project/vibeD/internal/auth"
        "github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/k8s"
	"github.com/vibed-project/vibeD/internal/metrics"
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
func NewHandler(deploySvc *deploy.Service, cfg *config.Config, bus *events.EventBus, m *metrics.Metrics, userStore store.UserStore, k8sClients *k8s.Clients) http.Handler {
        mux := http.NewServeMux()

        // API documentation (Swagger UI)
        mux.Handle("/api/docs", http.RedirectHandler("/api/docs/", http.StatusMovedPermanently))
        mux.Handle("/api/docs/", http.StripPrefix("/api/docs", swaggerUIHandler()))

        // SSE event stream
        mux.HandleFunc("/api/events", handleSSE(bus, m))

        // API routes. The artifact lifecycle now lives entirely under /v1/apps;
        // the legacy /api/artifacts and /api/targets surfaces were removed with
        // the orchestrator.
        mux.HandleFunc("/api/whoami", handleWhoami(userStore))
        // Auth-mode discovery for the SPA login screen: which auth mode is
        // active and where a browser login flow starts (e.g. SAML). Public by
        // design — the login UX needs the mode BEFORE authenticating; it leaks
        // nothing beyond what the login endpoints themselves reveal.
        vibedauth.RegisterPublicPrefix("/api/auth")
        mux.HandleFunc("/api/auth", handleAuthInfo(cfg))
        mux.HandleFunc("/api/organization", handleOrganization(cfg))
        mux.HandleFunc("/api/users", handleUsers(userStore))
        mux.HandleFunc("/api/users/", handleUserDetail(userStore))
        mux.HandleFunc("/api/departments", handleDepartments(userStore, k8sClients))
        mux.HandleFunc("/api/departments/", handleDepartmentDetail(userStore, k8sClients))
	// Share link routes (public — auth bypassed in SkipAuthPaths)
	mux.HandleFunc("/api/share/", handlePublicShareLink(deploySvc))
	mux.HandleFunc("/api/share-links/", handleShareLinkRevoke(deploySvc))

	// Browser-friendly share link page — serves the SPA so React renders ShareLinkPage.
	// The React app detects /share/<token> and calls /api/share/<token> as JSON.
	mux.HandleFunc("/share/", handleSPAIndex())

	// In-app navigation routes that need React to render (Settings, How to
	// connect). Without this, deep-linking or reloading the page would 404
	// because the file server doesn't know about these client-side routes.
	mux.HandleFunc("/settings", handleSPAIndex())
	mux.HandleFunc("/settings/", handleSPAIndex())
	mux.HandleFunc("/connect", handleSPAIndex())
	mux.HandleFunc("/connect/", handleSPAIndex())

	// Serve static frontend files. staticCacheControl forces revalidation of the
	// HTML shell so a redeploy (new content-hashed asset names) is picked up
	// immediately instead of a stale cached index.html pinning the old bundle.
	staticFS, _ := fs.Sub(StaticFiles, "static")
	mux.Handle("/", staticCacheControl(http.FileServer(http.FS(staticFS))))

	// Wrap with request body size limit for API endpoints (64MB for deploy, default for safety)
	return limitRequestBody(mux, cfg.Limits.MaxTotalFileSize)
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

// handleAuthInfo tells the SPA which auth mode is active and, for modes with a
// browser login flow, where that flow starts. The SPA uses this on a 401 to
// send the user to SSO instead of showing an API-key prompt that can't work.
func handleAuthInfo(cfg *config.Config) http.HandlerFunc {
	// Browser-login entrypoints by mode. Bearer-style modes (apikey, oidc)
	// have none — the SPA shows its token form / relies on a fronting proxy.
	loginURLs := map[string]string{
		"saml": "/saml/login",
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		info := map[string]any{
			"enabled": cfg.Auth.Enabled,
			"mode":    "",
			"loginUrl": "",
		}
		if cfg.Auth.Enabled {
			info["mode"] = cfg.Auth.Mode
			info["loginUrl"] = loginURLs[cfg.Auth.Mode]
		}
		json.NewEncoder(w).Encode(info)
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
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			users, err := userStore.ListUsers(r.Context(), departmentID, limit, offset)
			if err != nil {
				writeError(w, err, http.StatusInternalServerError)
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
				writeError(w, err, http.StatusConflict)
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
				writeError(w, err, http.StatusInternalServerError)
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
				writeError(w, err, http.StatusInternalServerError)
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

func handleDepartments(userStore store.UserStore, k8sClients *k8s.Clients) http.HandlerFunc {
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
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			depts, err := userStore.ListDepartments(r.Context(), limit, offset)
			if err != nil {
				writeError(w, err, http.StatusInternalServerError)
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
			nsName := "vibed-dept-" + strings.ToLower(strings.ReplaceAll(body.Name, " ", "-"))
			dept := &api.Department{
			        ID:        fmt.Sprintf("dept-%x", now.UnixNano()),
			        Name:      body.Name,
			        Namespace: nsName,
			        CreatedAt: now,
			        UpdatedAt: now,
			}

			if k8sClients != nil {
			        nsObj := &corev1.Namespace{
			                ObjectMeta: metav1.ObjectMeta{
			                        Name: nsName,
			                        Labels: map[string]string{
			                                "vibed.dev/tenant": dept.ID,
			                        },
			                },
			        }
			        if _, err := k8sClients.Clientset.CoreV1().Namespaces().Create(r.Context(), nsObj, metav1.CreateOptions{}); err != nil {
			                writeError(w, fmt.Errorf("failed to provision namespace: %w", err), http.StatusInternalServerError)
			                return
			        }
			}

			if err := userStore.CreateDepartment(r.Context(), dept); err != nil {
			        // Attempt rollback if db save fails
			        if k8sClients != nil {
			                _ = k8sClients.Clientset.CoreV1().Namespaces().Delete(context.Background(), nsName, metav1.DeleteOptions{})
			        }
			        writeError(w, err, http.StatusConflict)
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

func handleDepartmentDetail(userStore store.UserStore, k8sClients *k8s.Clients) http.HandlerFunc {
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
				writeError(w, err, http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(dept)

		case http.MethodDelete:
		        // Get department to find namespace
		        dept, err := userStore.GetDepartment(r.Context(), deptID)
		        if err != nil {
		                http.Error(w, "not found", http.StatusNotFound)
		                return
		        }

		        if err := userStore.DeleteDepartment(r.Context(), deptID); err != nil {
		                writeError(w, err, http.StatusInternalServerError)
		                return
		        }

		        // Cascade delete K8s namespace
		        if k8sClients != nil && dept.Namespace != "" {
		                _ = k8sClients.Clientset.CoreV1().Namespaces().Delete(context.Background(), dept.Namespace, metav1.DeleteOptions{})
		        }

		        w.Header().Set("Content-Type", "application/json")
		        json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// --- Share Link handlers ---

// staticCacheControl tags responses so browsers cache content-hashed assets
// aggressively but always revalidate the HTML shell. Without this the shell has
// no cache headers and browsers heuristically cache it; after a redeploy the
// asset hashes change and a stale index.html keeps referencing a bundle that no
// longer exists, leaving the app blank/unresponsive until a hard reload.
func staticCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case strings.HasPrefix(p, "/assets/"):
			// Vite emits immutable content-hashed filenames here.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case p == "/" || strings.HasSuffix(p, "/") || strings.HasSuffix(p, ".html"):
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		next.ServeHTTP(w, r)
	})
}

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
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		http.ServeContent(w, r, "index.html", stat.ModTime(), f.(interface {
			io.ReadSeeker
		}))
	}
}

// DELETE /api/share-links/{token} — revoke a share link
func handleShareLinkRevoke(deploySvc *deploy.Service) http.HandlerFunc {
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

		owner := vibedauth.UserIDFromContext(r.Context())
		if deploySvc == nil || deploySvc.ShareLinks == nil || owner == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := deploySvc.RevokeShareLink(r.Context(), owner, token); err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
	}
}

// GET/POST /api/share/{token} — public share link resolution
func handlePublicShareLink(deploySvc *deploy.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, "/api/share/")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}

		var password string
		if r.Method == http.MethodPost {
			var body struct {
				Password string `json:"password"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			password = body.Password
		}

		if deploySvc == nil || deploySvc.ShareLinks == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// VibedApp path: resolve the token to the app's URL. A wrong/missing
		// password is a 401; a missing/invalid token is a 404.
		app, err := deploySvc.ResolveShareLink(r.Context(), token, password)
		if err != nil {
			var pwReq *api.ErrPasswordRequired
			if errors.As(err, &pwReq) {
				writeError(w, err, http.StatusUnauthorized)
				return
			}
			writeError(w, err, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":   app.Name,
			"status": string(app.Status.Phase),
			"url":    app.Status.URL,
			"target": "app",
		})
	}
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
