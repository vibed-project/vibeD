// Package tenant defines the isolation scope a request runs in.
//
// The core runs a single implicit tenant — one namespace, no per-tenant limits —
// so behavior is identical to a pre-tenancy build. An out-of-tree module can
// install a multi-tenant Resolver (via pkg/plugin.RegisterTenantResolver) that
// maps each request to its own namespace and limits, which is the foundation for
// running vibeD for more than one tenant.
package tenant

import "context"

// Limits are per-tenant resource ceilings. Zero means unlimited.
type Limits struct {
	// MaxApps caps the total number of live apps in the tenant (0 = unlimited).
	MaxApps int
}

// Tenant is the scope a request runs in: a stable ID, the Kubernetes namespace
// its VibedApps live in, and its resource limits. The zero Tenant is the
// single-tenant default (empty ID, the server-default namespace, no limits).
type Tenant struct {
	ID        string
	Namespace string
	Limits    Limits
}

// Resolver maps a request context to its Tenant. Implementations read whatever
// the request carries (a bearer token, an OIDC issuer, a header) to identify the
// tenant. The core installs Single; an out-of-tree module installs its own.
type Resolver interface {
	Resolve(ctx context.Context) (Tenant, error)
}

// Single returns a Resolver that yields t for every request — the single-tenant
// default, so the core behaves exactly as it did before tenancy existed.
func Single(t Tenant) Resolver { return single{t} }

type single struct{ t Tenant }

func (s single) Resolve(context.Context) (Tenant, error) { return s.t, nil }
