package auth

import (
	"encoding/json"
	"net/http"

	"github.com/vibed-project/vibeD/internal/config"
)

// OAuthProtectedResourceMetadata represents RFC 9728 metadata.
type OAuthProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// OAuthMetadataHandler returns an http.HandlerFunc that serves the
// /.well-known/oauth-protected-resource metadata per RFC 9728.
// MCP clients use this to discover which authorization server to authenticate against.
func OAuthMetadataHandler(cfg config.OIDCConfig, resourceURL string) http.HandlerFunc {
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile"}
	}

	meta := OAuthProtectedResourceMetadata{
		Resource:               resourceURL,
		AuthorizationServers:   []string{cfg.Issuer},
		ScopesSupported:        scopes,
		BearerMethodsSupported: []string{"header"},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		json.NewEncoder(w).Encode(meta)
	}
}
