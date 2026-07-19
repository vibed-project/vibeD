// Package authz is the per-action authorization seam.
//
// The core's built-in authorization is a binary role (admin|user) plus owner
// equality and a shared-with read list, enforced inline in the orchestrator and
// deploy service. That is sufficient for single-user and small-team use but
// cannot express scoped, fine-grained permissions (who may deploy/delete/
// rollback/share/administer, per team or namespace).
//
// An Authorizer, when registered, REPLACES the inline owner/admin decision at
// the artifact chokepoints: it is consulted for each action on each resource and
// may allow access the inline check would deny (e.g. a teammate) or deny access
// it would allow. The core installs no Authorizer, so behavior is unchanged
// until an out-of-tree module (e.g. vibeD Enterprise's RBAC) registers one.
package authz

import (
	"context"
	"sync"
)

// Action identifies the operation being authorized. Values are stable strings
// so an Authorizer (and audit records) can key on them.
type Action string

const (
	ActionAppGet      Action = "app.get"      // read a single app
	ActionAppList     Action = "app.list"     // list apps
	ActionAppDeploy   Action = "app.deploy"   // create or redeploy
	ActionAppDelete   Action = "app.delete"   // delete
	ActionAppRollback Action = "app.rollback" // roll back to a prior version
	ActionAppSuspend  Action = "app.suspend"  // suspend/resume
	ActionAppShare    Action = "app.share"    // share/unshare, manage share links
	ActionUserManage  Action = "user.manage"  // manage users/departments
	ActionAuditRead   Action = "audit.read"   // read the audit trail
)

// Resource describes the object an action targets. For app actions it is the
// artifact/VibedApp; ID is empty for collection actions such as list.
type Resource struct {
	Kind       string   // "app" (the only kind today)
	ID         string   // artifact/app id ("" for list)
	Owner      string   // owning user id
	Namespace  string   // tenant/team namespace the resource lives in
	Department string   // department label, when known (deploy path)
	SharedWith []string // user ids granted read via sharing
}

// Request is a single authorization question: may Subject (whose core Role is
// carried for backward-compatible admin handling) perform Action on Resource?
type Request struct {
	Subject  string // caller user id ("" when unauthenticated)
	Role     string // caller's core role, e.g. "admin" | "user"
	Action   Action
	Resource Resource
}

// Authorizer decides a single Request. A nil return allows the action; a
// non-nil error denies it. Implementations should return *DeniedError (or any
// error whose Forbidden() reports true) so the API maps the denial to 403.
type Authorizer interface {
	Authorize(ctx context.Context, req Request) error
}

// DeniedError marks an authorization denial. The API detects it via Forbidden()
// (mirroring policy's PolicyDenied() / quota's QuotaExceeded()) and returns 403.
// Reason should name the missing permission for the audit record.
type DeniedError struct {
	Action Action
	Reason string
}

func (e *DeniedError) Error() string {
	if e.Reason != "" {
		return "authorization denied: " + string(e.Action) + ": " + e.Reason
	}
	return "authorization denied: " + string(e.Action)
}

// Forbidden reports that this error is an authorization denial (→ HTTP 403).
func (e *DeniedError) Forbidden() bool { return true }

// IsDenied reports whether err is (or wraps) an authorization denial.
func IsDenied(err error) bool {
	type forbidden interface{ Forbidden() bool }
	for err != nil {
		if f, ok := err.(forbidden); ok && f.Forbidden() {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// --- registry ---------------------------------------------------------------

// Deps is what an Authorizer factory receives; Options is a generic settings bag.
type Deps struct {
	Options map[string]string
}

// Factory builds the process Authorizer. There is at most one.
type Factory func(Deps) (Authorizer, error)

var (
	mu      sync.Mutex
	factory Factory
)

// Register installs the authorizer factory. Called from an out-of-tree module's
// init(); it panics if one is already registered.
func Register(f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if factory != nil {
		panic("authz: an authorizer factory is already registered")
	}
	factory = f
}

// Build returns the registered Authorizer, or nil when none is registered
// (meaning the core's built-in owner/admin checks stay in effect).
func Build(deps Deps) (Authorizer, error) {
	mu.Lock()
	f := factory
	mu.Unlock()
	if f != nil {
		return f(deps)
	}
	return nil, nil
}
