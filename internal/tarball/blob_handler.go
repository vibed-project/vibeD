package tarball

import (
	"net/http"
	"strings"

	"github.com/vibed-project/vibeD/internal/config"
)

// NewBlobHandler returns an http.Handler that serves the served-backend's
// tarballs at BlobPathPrefix (GET /internal/sources/<id>.tar.gz). vibed-agent
// fetches source from here when the served backend is in use.
//
// Returns (nil, nil) when the configured backend isn't "served" — there's
// nothing for vibeD to serve in the S3 case (the agent pulls from object
// storage directly). Authentication is the caller's responsibility: mount
// this behind the same auth middleware that protects /v1 so only the shared
// agent token can pull.
func NewBlobHandler(cfg config.TarballConfig) (http.Handler, error) {
	if cfg.Backend != "" && cfg.Backend != "served" {
		return nil, nil
	}
	if cfg.Served.BasePath == "" {
		return nil, nil
	}
	// http.FileServer over the base dir; strip the URL prefix so
	// /internal/sources/<id>.tar.gz maps to <basePath>/<id>.tar.gz.
	fs := http.FileServer(http.Dir(cfg.Served.BasePath))
	stripped := http.StripPrefix(strings.TrimRight(BlobPathPrefix, "/"), fs)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Only serve *.tar.gz; never expose directory listings or other files.
		if !strings.HasSuffix(r.URL.Path, ".tar.gz") {
			http.NotFound(w, r)
			return
		}
		stripped.ServeHTTP(w, r)
	}), nil
}
