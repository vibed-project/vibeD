package egressauthz

import (
	"io"
	"log/slog"
	"net/http"
)

// Handler answers per-connection egress authorization for the egress proxy.
// GET /authz?src=<pod IP>&host=<dst host> → 200 (allow) / 403 (deny).
type Handler struct {
	resolver    Resolver
	systemHosts []string
	logger      *slog.Logger
}

// NewHandler builds the authz HTTP handler. systemHosts are always allowed
// (e.g. the source store) regardless of any app's allow-list.
func NewHandler(resolver Resolver, systemHosts []string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{resolver: resolver, systemHosts: systemHosts, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("/authz", h.authz)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	return mux
}

func (h *Handler) authz(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")
	host := r.URL.Query().Get("host")

	allowed, _ := h.resolver.AllowedFor(r.Context(), src)
	if Authorize(h.systemHosts, allowed, host) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "OK")
		return
	}
	// Default-deny. Logged for the audit trail (sub-area 3 will persist these).
	h.logger.Info("egress denied", "src", src, "host", host)
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, "DENY")
}
