package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/templatevalidate"
)

// appLabelKey marks every object the controller creates on behalf of a
// VibedApp: per-app Services (service.go) and SandboxClaims (claim.go) carry
// it, and have since those code paths first landed — which is what makes the
// label-EXISTS cache scopes in ManagerCacheOptions safe across upgrades.
const appLabelKey = "vibed.dev/app"

// ManagerCacheOptions scopes the manager's informer cache to the objects the
// controller actually reads. Without it, the first cached Get of a GVK starts
// an all-namespaces, full-object informer for that type — reading one per-app
// Service would cache every Service in the cluster.
//
// A scoped cache silently returns NotFound for objects outside its selector,
// so only GVKs whose every read path is provably covered are narrowed (the
// audit lives in cachescope_test.go):
//
//   - Service: reconcileSelector is the only Service read, and it only ever
//     Gets Services EnsureService itself created — all labeled vibed.dev/app.
//   - SandboxClaim (including the Watches() informer): claim.go and
//     service.go only read claims SandboxClaimer created — all labeled
//     vibed.dev/app. Filtering the watch also stops non-vibeD claims from
//     enqueuing phantom VibedApp reconciles.
//   - Pod: only templatevalidate reads Pods, always in the pool namespace and
//     always list-selecting on vibed.dev/template — so the identical cache
//     scope cannot hide a Pod those reads would match.
//   - ConfigMap: only the vibed-template-validation ConfigMap in the pool
//     namespace is ever read (gate Allowed + validator Persist).
//
// Deliberately unscoped:
//
//   - VibedApp: our own CRD — the controller must see them all.
//   - SandboxTemplate: read in app namespaces (claim fail-fast) and the pool
//     namespace (validator list), and not labeled by vibeD; one object per
//     slot, so cluster-wide caching is cheap.
func ManagerCacheOptions(poolNamespace string) cache.Options {
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(SandboxClaimGVK)

	appManaged := labelExists(appLabelKey)
	warmPod := labelExists(templatevalidate.TemplateLabelKey)
	validationCM := fields.OneTermEqualSelector("metadata.name", templatevalidate.ConfigMapName)

	// Pod/ConfigMap reads are additionally bounded to the pool namespace. Guard
	// against an empty value: "" is cache.AllNamespaces, so an empty-keyed
	// Namespaces entry would not mean what it looks like — fall back to the
	// selector-only scope instead.
	podScope := cache.ByObject{Label: warmPod}
	cmScope := cache.ByObject{Field: validationCM}
	if poolNamespace != "" {
		podScope = cache.ByObject{Namespaces: map[string]cache.Config{
			poolNamespace: {LabelSelector: warmPod},
		}}
		cmScope = cache.ByObject{Namespaces: map[string]cache.Config{
			poolNamespace: {FieldSelector: validationCM},
		}}
	}

	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Service{}:   {Label: appManaged},
			claim:               {Label: appManaged},
			&corev1.Pod{}:       podScope,
			&corev1.ConfigMap{}: cmScope,
		},
	}
}

// labelExists builds a selector matching any object that carries the label
// key, regardless of value. Panics only on an invalid key, which cannot
// happen for the compile-time constants this file passes.
func labelExists(key string) labels.Selector {
	req, err := labels.NewRequirement(key, selection.Exists, nil)
	if err != nil {
		panic(err)
	}
	return labels.NewSelector().Add(*req)
}
