// Optional list-path extensions to the Authorizer seam.
//
// Authorize (one Request, one decision) remains the semantics of record for
// every access check. The interfaces below are purely performance seams for
// the list path: a fine-grained Authorizer that does I/O per decision would
// otherwise be consulted once per app in the namespace on every List. Both
// are OPTIONAL and discovered by type assertion on the registered Authorizer,
// so the Authorizer interface itself is unchanged and existing implementors
// keep working as-is.
//
// (The package doc lives in authz.go.)

package authz

import (
	"context"

	"k8s.io/apimachinery/pkg/labels"
)

// ListScoper is an optional interface an Authorizer may additionally
// implement to narrow list queries server-side, before per-item
// authorization.
//
// ScopeList receives a collection Request (Action ActionAppList;
// Resource.ID empty, Resource.Namespace set to the namespace being listed)
// and returns a label selector the caller applies to the underlying List.
//
// The selector is a COARSE PRE-FILTER, not an authorization decision: every
// candidate it returns is still exactly filtered by Authorize (or
// AuthorizeBatch), so a lossy, OVER-returning selector is always safe — it
// merely fetches more than needed. The implementor's contract is the reverse
// bound: the selector MUST match at least every app the subject is authorized
// to see. An UNDER-returning selector silently hides authorized apps, because
// anything it excludes is never fetched and therefore never even reaches
// Authorize. When in doubt, return a broader selector — or none at all.
//
// Returning a nil selector (with a nil error) declines narrowing: the caller
// lists the whole namespace, exactly as if ScopeList were not implemented. A
// non-nil error aborts the list and is returned to the caller.
type ListScoper interface {
	ScopeList(ctx context.Context, req Request) (labels.Selector, error)
}

// BatchAuthorizer is an optional interface an Authorizer may additionally
// implement to decide many Requests in one call (one round trip to a policy
// backend instead of one per candidate app).
//
// The result slice is parallel to reqs: result[i] is the decision for
// reqs[i], nil meaning allowed and a non-nil error (ideally *DeniedError)
// meaning denied — the Authorize contract, applied element-wise.
//
// AuthorizeBatch MUST produce, for each element, the same decision Authorize
// would produce for that Request alone: per-item Authorize remains the
// semantics of record and the batch is only an execution strategy.
// Implementors MUST return a slice of exactly len(reqs); callers MUST treat
// any length mismatch as an implementation error and fall back to per-item
// Authorize rather than guess at alignment.
type BatchAuthorizer interface {
	AuthorizeBatch(ctx context.Context, reqs []Request) []error
}
