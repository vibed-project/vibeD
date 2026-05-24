package controller

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/templatevalidate"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// ErrTemplateNotFound is returned by EnsureClaim when no SandboxTemplate backs
// the app's slot — a terminal condition (the claim could never bind). The
// reconciler maps it to Failed/TemplateMissing rather than retrying forever.
var ErrTemplateNotFound = errors.New("no SandboxTemplate for template")

// SandboxClaimGVK identifies the agent-sandbox SandboxClaim CR vibeD
// targets. Pinned to v1alpha1 per the kubernetes-sigs/agent-sandbox v0.4.5
// extensions bundle; see the agent-sandbox-crds memory entry for the full
// schema.
var SandboxClaimGVK = schema.GroupVersionKind{
	Group:   "extensions.agents.x-k8s.io",
	Version: "v1alpha1",
	Kind:    "SandboxClaim",
}

// SandboxClaimer is the production Claimer implementation. It creates one
// SandboxClaim per VibedApp (idempotently) and reports back whether
// agent-sandbox has bound a Sandbox pod yet.
//
// Naming convention: the SandboxClaim name equals the VibedApp name, scoped
// to the same Namespace. That gives us a 1:1 mapping the watch hookup uses
// to map claim events back to the owning VibedApp.
//
// Warm-pool naming: refactor.md's template.yaml files create a
// SandboxWarmPool whose name matches the template name (node-24,
// python-313, etc.). So `sandboxTemplateRef.name = template` and
// `warmpool = template` both flow from the classifier's pick.
type SandboxClaimer struct {
	Client client.Client
	// PoolNamespace is where the SandboxWarmPool / SandboxTemplate live.
	// Defaults to "vibed-pools" (matching templates/*/template.yaml). The
	// SandboxClaim itself is created in the same namespace as the
	// VibedApp it serves — agent-sandbox supports cross-namespace pool
	// references.
	PoolNamespace string
}

// EnsureClaim creates the SandboxClaim if it doesn't exist, reads its
// status, and returns the binding state.
func (c *SandboxClaimer) EnsureClaim(ctx context.Context, app *vibedv1.VibedApp) (bool, string, string, error) {
	template := app.Spec.Runtime.Template
	if template == "" {
		return false, "", "", fmt.Errorf("VibedApp %s/%s: spec.runtime.template is required to claim", app.Namespace, app.Name)
	}

	// Fail fast when no warm pool backs this slot. The classifier can pick a
	// destination that isn't deployed (e.g. "spin"), or an operator can typo a
	// custom template; without this the SandboxClaim would never bind and the
	// app would sit in Claiming forever. The SandboxTemplate is resolved in the
	// claim's (= app's) namespace.
	tmpl := &unstructured.Unstructured{}
	tmpl.SetGroupVersionKind(templatevalidate.SandboxTemplateGVK)
	if err := c.Client.Get(ctx, types.NamespacedName{Name: template, Namespace: app.Namespace}, tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "", "", fmt.Errorf("%w %q (no warm pool configured for it)", ErrTemplateNotFound, template)
		}
		return false, "", "", fmt.Errorf("get SandboxTemplate %s/%s: %w", app.Namespace, template, err)
	}

	key := types.NamespacedName{Name: app.Name, Namespace: app.Namespace}
	got := newClaim()
	err := c.Client.Get(ctx, key, got)
	switch {
	case err == nil:
		// Claim exists — read status.
		return readClaimStatus(got)
	case apierrors.IsNotFound(err):
		// Create the claim. Body is built fresh; agent-sandbox fills in
		// the rest async.
		desired := c.buildClaim(app, template)
		if err := c.Client.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, "", "", fmt.Errorf("create SandboxClaim %s/%s: %w", app.Namespace, app.Name, err)
		}
		// On a fresh create the status is necessarily empty — return
		// not-bound so the reconciler requeues and reads the real status
		// on the next pass.
		return false, "", "", nil
	default:
		return false, "", "", fmt.Errorf("get SandboxClaim %s/%s: %w", app.Namespace, app.Name, err)
	}
}

// ReleaseClaim deletes the app's SandboxClaim, which agent-sandbox interprets
// as returning the bound pod to the warm pool. Idempotent — a missing claim is
// treated as already released.
func (c *SandboxClaimer) ReleaseClaim(ctx context.Context, app *vibedv1.VibedApp) error {
	claim := newClaim()
	claim.SetName(app.Name)
	claim.SetNamespace(app.Namespace)
	if err := c.Client.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete SandboxClaim %s/%s: %w", app.Namespace, app.Name, err)
	}
	return nil
}

func (c *SandboxClaimer) buildClaim(app *vibedv1.VibedApp, template string) *unstructured.Unstructured {
	u := newClaim()
	u.SetName(app.Name)
	u.SetNamespace(app.Namespace)
	u.SetLabels(map[string]string{
		"vibed.dev/app":      app.Name,
		"vibed.dev/owner":    app.Spec.Owner,
		"vibed.dev/template": template,
	})
	// Owner reference: when the VibedApp is deleted, K8s reaps the claim
	// (which agent-sandbox interprets as a release back to the pool).
	u.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion:         vibedv1.GroupVersion.String(),
			Kind:               "VibedApp",
			Name:               app.Name,
			UID:                app.UID,
			Controller:         ptrBool(true),
			BlockOwnerDeletion: ptrBool(true),
		},
	})
	spec := map[string]any{
		"sandboxTemplateRef": map[string]any{
			"name": template,
		},
		"warmpool": template,
	}
	// Push the user's env through to the claim — agent-sandbox forwards
	// these into the user-container env at bind time.
	if envs := app.Spec.Runtime.Env; len(envs) > 0 {
		flat := make([]any, 0, len(envs))
		for _, e := range envs {
			flat = append(flat, map[string]any{
				"name":  e.Name,
				"value": e.Value,
			})
		}
		spec["env"] = flat
	}
	_ = unstructured.SetNestedField(u.Object, spec, "spec")
	return u
}

// readClaimStatus pulls .status.sandbox.{name,podIPs[0]} out of an
// Unstructured claim. Returns (bound=false, "", "") when the binding
// hasn't happened yet — there's no separate "still claiming" condition we
// trust, so absence of the name+ip is the signal.
func readClaimStatus(u *unstructured.Unstructured) (bool, string, string, error) {
	name, found, err := unstructured.NestedString(u.Object, "status", "sandbox", "name")
	if err != nil {
		return false, "", "", fmt.Errorf("read claim status.sandbox.name: %w", err)
	}
	if !found || name == "" {
		return false, "", "", nil
	}
	ips, found, err := unstructured.NestedStringSlice(u.Object, "status", "sandbox", "podIPs")
	if err != nil {
		return false, "", "", fmt.Errorf("read claim status.sandbox.podIPs: %w", err)
	}
	if !found || len(ips) == 0 {
		// Sandbox bound but the pod hasn't been assigned an IP yet —
		// treat as not-yet-bound; the next reconcile will pick it up.
		return false, "", "", nil
	}
	return true, name, ips[0], nil
}

func newClaim() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(SandboxClaimGVK)
	return u
}

func ptrBool(b bool) *bool { return &b }
