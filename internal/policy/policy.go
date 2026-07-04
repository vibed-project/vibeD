// Package policy is the deploy-time policy seam.
//
// A Gate evaluates each deploy after classification and before the VibedApp is
// created, and may deny it. The core installs no Gate, so deploys are
// unrestricted and behavior is unchanged; an out-of-tree module can register one
// to enforce governance (allowed templates/registries/egress, guardrails on
// generated deploys, secrets-in-source, resource ceilings, …).
package policy

import (
	"context"
	"sync"

	"github.com/vibed-project/vibeD/internal/tenant"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// Input is the deploy decision a Gate evaluates.
type Input struct {
	Tenant       tenant.Tenant
	Owner        string
	Name         string
	Lane         vibedv1.Lane
	Template     string
	AllowedHosts []string
	Env          []vibedv1.EnvVar
	IsNew        bool
	// Source is the uploaded source tarball (gzip'd), for content policies such
	// as secret scanning. Read-only; do not retain it past Evaluate.
	Source []byte
}

// Gate evaluates a deploy. A non-nil error denies it; implementations should
// return a *DeniedError (or any error whose PolicyDenied() is true) so the API
// maps the denial to HTTP 403.
type Gate interface {
	Evaluate(ctx context.Context, in Input) error
}

// DeniedError marks a policy denial. The deploy API detects it via the
// PolicyDenied() method (mirroring quota's QuotaExceeded()) and returns 403.
type DeniedError struct {
	Reason string
}

func (e *DeniedError) Error() string      { return "deploy denied by policy: " + e.Reason }
func (e *DeniedError) PolicyDenied() bool { return true }

// --- registry ---------------------------------------------------------------

// Deps is what a Gate factory receives; Options is a generic settings bag.
type Deps struct {
	Options map[string]string
}

// Factory builds the process Gate. There is at most one.
type Factory func(Deps) (Gate, error)

var (
	mu      sync.Mutex
	factory Factory
)

// Register installs the gate factory. Called from an out-of-tree module's
// init(); it panics if one is already registered.
func Register(f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if factory != nil {
		panic("policy: a gate factory is already registered")
	}
	factory = f
}

// Build returns the registered Gate, or nil when none is registered (meaning no
// policy — deploys are unrestricted).
func Build(deps Deps) (Gate, error) {
	mu.Lock()
	f := factory
	mu.Unlock()
	if f != nil {
		return f(deps)
	}
	return nil, nil
}
