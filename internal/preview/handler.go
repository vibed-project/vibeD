// Package preview implements vibeD's reverse-proxy for fast-path previews.
//
// A fast-path runner Sandbox only has an in-cluster URL
// (`http://<runner>.<ns>.svc.cluster.local:8080`), so users outside the cluster
// cannot reach a preview directly. This handler mounts at `/preview/<id>/...`
// on vibeD's HTTP server and proxies in-cluster to the runner pod, so previews
// are reachable through whatever ingress already serves vibeD itself.
//
// The handler relies on the standard auth middleware to authenticate callers
// (see internal/auth.SkipAuthPaths) and then on Orchestrator.Status to enforce
// per-artifact ownership / share permissions before proxying.
package preview

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/vibed-project/vibeD/pkg/api"
)

// PathPrefix is the URL prefix the handler mounts under.
const PathPrefix = "/preview/"

// artifactSource is the slice of orchestrator the handler depends on; an
// interface so the handler can be tested without standing up an orchestrator.
type artifactSource interface {
	Status(ctx context.Context, artifactID string) (*api.Artifact, error)
}

// Handler proxies HTTP requests to a fast-path preview's runner pod.
type Handler struct {
	src    artifactSource
	logger *slog.Logger
}

// New returns a Handler that resolves artifacts via src.
func New(src artifactSource, logger *slog.Logger) *Handler {
	return &Handler{src: src, logger: logger}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	artifactID, upstreamPath, ok := splitPreviewPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// /preview/<id> with no trailing slash → redirect so relative URLs in the
	// proxied page (e.g. "/style.css") resolve under /preview/<id>/, not /.
	if upstreamPath == "" {
		dest := r.URL.Path + "/"
		if r.URL.RawQuery != "" {
			dest += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, dest, http.StatusMovedPermanently)
		return
	}

	artifact, err := h.src.Status(r.Context(), artifactID)
	if err != nil {
		// Be vague — don't leak whether the artifact exists vs. you can't see it.
		http.NotFound(w, r)
		return
	}
	if artifact.Mode != api.ModePreview || artifact.Target != api.TargetRunner {
		http.Error(w, "not a fast-path preview artifact", http.StatusBadRequest)
		return
	}
	if artifact.Status != api.StatusRunning {
		http.Error(w, fmt.Sprintf("preview is %s, not running", artifact.Status), http.StatusServiceUnavailable)
		return
	}
	if artifact.URL == "" {
		http.Error(w, "preview URL not set on artifact", http.StatusServiceUnavailable)
		return
	}

	target, err := url.Parse(artifact.URL)
	if err != nil {
		h.logger.Error("invalid runner URL", "artifact_id", artifactID, "url", artifact.URL, "error", err)
		http.Error(w, "preview misconfigured", http.StatusInternalServerError)
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = upstreamPath
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = target.Host
			// The user app must not see vibeD's bearer token / session cookies.
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			// Forwarding hints — apps that honour X-Forwarded-Prefix (Werkzeug
			// ProxyFix, FastAPI root_path) can reconstruct correct URLs under
			// the /preview/<id> mount. ReverseProxy already handles
			// X-Forwarded-For; the others are not added automatically.
			req.Header.Set("X-Forwarded-Host", r.Host)
			proto := "http"
			if r.TLS != nil {
				proto = "https"
			}
			req.Header.Set("X-Forwarded-Proto", proto)
			req.Header.Set("X-Forwarded-Prefix", strings.TrimSuffix(PathPrefix, "/")+"/"+artifactID)
			req.Header.Set("X-Vibed-Artifact-Id", artifactID)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, perr error) {
			h.logger.Warn("preview proxy error", "artifact_id", artifactID, "error", perr)
			http.Error(w, "preview unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// splitPreviewPath parses /preview/<id>/<rest> into (id, "/<rest>", ok).
// /preview/<id> with no trailing segment returns ("<id>", "", true) so the
// caller can issue a redirect; anything not matching returns ("", "", false).
func splitPreviewPath(path string) (artifactID, upstream string, ok bool) {
	rest := strings.TrimPrefix(path, PathPrefix)
	if rest == path || rest == "" {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		return "", "", false
	}
	if len(parts) == 1 {
		return id, "", true
	}
	return id, "/" + parts[1], true
}
