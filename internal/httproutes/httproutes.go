// Package httproutes is the seam for out-of-tree modules to contribute
// additional HTTP routes to the control-plane server.
//
// Unlike the auth package's login Routes (which are public, for SSO callbacks),
// routes registered here are mounted on the main mux and therefore run behind
// the authentication + role middleware — the caller's identity and role are in
// the request context, so a handler can enforce its own RBAC. The core
// registers none; an enterprise module uses this to expose, e.g., a
// role-management API. Multiple modules may register.
package httproutes

import (
	"net/http"
	"sync"
)

// Route is an authenticated HTTP route (a Go 1.22 pattern + handler) mounted on
// the control-plane mux.
type Route struct {
	Pattern string // e.g. "POST /v1/rolebindings"
	Handler http.Handler
	// Public exempts this route's path from the core's user-auth middleware, so
	// the module can enforce its own authentication (e.g. a SCIM provisioning
	// token). The server registers the path as a public prefix; the module's
	// handler is responsible for authenticating the request itself.
	Public bool
}

// Deps is what a route factory receives; Options is a generic settings bag.
type Deps struct {
	Options map[string]string
}

// Factory builds a module's routes. Multiple may be registered.
type Factory func(Deps) ([]Route, error)

var (
	mu        sync.Mutex
	factories []Factory
)

// Register adds a route factory. Called from an out-of-tree module's init().
func Register(f Factory) {
	mu.Lock()
	defer mu.Unlock()
	factories = append(factories, f)
}

// Build invokes every registered factory and returns the combined routes. The
// core registers none, so this is empty unless a module contributes.
func Build(deps Deps) ([]Route, error) {
	mu.Lock()
	fs := append([]Factory(nil), factories...)
	mu.Unlock()

	var routes []Route
	for _, f := range fs {
		rs, err := f(deps)
		if err != nil {
			return nil, err
		}
		routes = append(routes, rs...)
	}
	return routes, nil
}
