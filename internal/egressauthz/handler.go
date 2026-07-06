package egressauthz

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/vibed-project/vibeD/internal/netguard"
)

// resolvesToBlocked reports whether an allow-listed host resolves to the cloud
// metadata endpoint or another link-local/loopback address (DNS-rebinding
// defense-in-depth). It intentionally checks only link-local/loopback, NOT
// private/RFC1918: the egress authz legitimately allows in-cluster system hosts
// (e.g. the served source store), whose ClusterIPs are RFC1918 — denying those
// would 403 the source pull and hang deploys. Blanket private-range denial for
// the production posture lives in the Squid dst ACLs instead. It is a package
// var so tests can stub it without real DNS.
var resolvesToBlocked = func(ctx context.Context, host string) (bool, error) {
	return netguard.HostResolvesToLinkLocalOrLoopback(ctx, nil, host)
}

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
		// Defense-in-depth against DNS rebinding: deny even an allow-listed host
		// if it resolves to the cloud metadata endpoint or an internal range.
		// The egress proxy also denies these by resolved IP; this catches a
		// misconfigured proxy. A resolution error is allowed through so authz
		// availability does not hinge on DNS — the proxy stays the hard gate.
		if blocked, err := resolvesToBlocked(r.Context(), normalizeHost(host)); err == nil && blocked {
			h.logger.Warn("egress denied: allow-listed host resolves to a blocked range", "src", src, "host", host)
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "DENY")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "OK")
		return
	}
	// Default-deny. Logged for the audit trail (sub-area 3 will persist these).
	h.logger.Info("egress denied", "src", src, "host", host)
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, "DENY")
}
