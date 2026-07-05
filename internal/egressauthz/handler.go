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

	// dns resolves a requested host to its IP addresses so the handler can
	// deny hosts that resolve into a private/link-local/loopback/metadata
	// range, regardless of the per-app allow-list (DNS-rebind defense).
	dns hostResolver

	// allowInternalHosts is the operator opt-out: exact hostnames (or the
	// same *.wildcard/apex patterns the allow-list uses) that are permitted
	// to resolve into an otherwise-blocked internal range. Empty by default,
	// i.e. every private/metadata range is denied.
	allowInternalHosts []string
}

// NewHandler builds the authz HTTP handler. systemHosts are always allowed
// (e.g. the source store) regardless of any app's allow-list. It applies the
// default private-IP deny policy (no internal-host opt-outs); use
// NewHandlerWithOptions to permit specific internal hosts.
func NewHandler(resolver Resolver, systemHosts []string, logger *slog.Logger) http.Handler {
	return NewHandlerWithOptions(resolver, systemHosts, nil, logger)
}

// NewHandlerWithOptions is NewHandler plus an operator opt-out list:
// allowInternalHosts are the only hostnames permitted to resolve into a
// private/link-local/loopback/metadata range. Everything else that resolves
// into those ranges is denied even if the per-app allow-list matches it.
func NewHandlerWithOptions(resolver Resolver, systemHosts, allowInternalHosts []string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{
		resolver:           resolver,
		systemHosts:        systemHosts,
		logger:             logger,
		dns:                netResolver{},
		allowInternalHosts: allowInternalHosts,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/authz", h.authz)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	return mux
}

func (h *Handler) authz(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")
	host := r.URL.Query().Get("host")

	allowed, _ := h.resolver.AllowedFor(r.Context(), src)
	if !Authorize(h.systemHosts, allowed, host) {
		h.deny(w, src, host, "not on allow-list")
		return
	}

	// Defense-in-depth: even an allow-listed (or system) host is denied if it
	// resolves into a private/link-local/loopback/metadata range, unless the
	// operator explicitly opted this host in. This blocks a DNS-rebinding
	// attack where an allow-listed domain points at 169.254.169.254 or an
	// internal service IP.
	if !Match(h.allowInternalHosts, host) {
		normalized := normalizeHost(host)
		if blocked, _ := resolvesToBlocked(r.Context(), h.dns, normalized); blocked {
			h.deny(w, src, host, "resolves to a private/metadata range")
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "OK")
}

func (h *Handler) deny(w http.ResponseWriter, src, host, reason string) {
	// Default-deny. Logged for the audit trail (sub-area 3 will persist these).
	h.logger.Info("egress denied", "src", src, "host", host, "reason", reason)
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, "DENY")
}
