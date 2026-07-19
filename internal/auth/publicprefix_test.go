package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSkipAuthPaths_PublicPrefix verifies a module-registered public prefix is
// exempt from the auth middleware, while protected prefixes still run it.
func TestSkipAuthPaths_PublicPrefix(t *testing.T) {
	var authRan bool
	authMw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authRan = true
			next.ServeHTTP(w, r)
		})
	}
	RegisterPublicPrefix("/scim/v2/")
	h := SkipAuthPaths(authMw)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		path     string
		wantAuth bool
		desc     string
	}{
		{"/scim/v2/Users", false, "registered public prefix skips auth"},
		{"/v1/apps", true, "protected prefix runs auth"},
		{"/healthz", false, "health is public"},
	}
	for _, c := range cases {
		authRan = false
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", c.path, nil))
		if authRan != c.wantAuth {
			t.Errorf("%s: authRan=%v, want %v", c.desc, authRan, c.wantAuth)
		}
	}
}
