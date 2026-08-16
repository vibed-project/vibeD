package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/templatevalidate"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// cacheVisible reports whether the informer cache built from opts would hold
// obj. It mirrors how controller-runtime applies cache.ByObject: the entry
// matching the object's GVK contributes namespace, label, and field
// restrictions, while GVKs without an entry are cached cluster-wide. The
// scoped-cache tests below seed fake clients with only cache-visible objects,
// reproducing the silent NotFound a scoped cache returns for everything else.
func cacheVisible(t *testing.T, opts cache.Options, s *runtime.Scheme, obj client.Object) bool {
	t.Helper()
	gvk, err := apiutil.GVKForObject(obj, s)
	if err != nil {
		t.Fatalf("gvk for %T: %v", obj, err)
	}
	lbls := labels.Set(obj.GetLabels())
	flds := fields.Set{"metadata.name": obj.GetName(), "metadata.namespace": obj.GetNamespace()}
	for key, spec := range opts.ByObject {
		keyGVK, err := apiutil.GVKForObject(key, s)
		if err != nil {
			t.Fatalf("gvk for ByObject key %T: %v", key, err)
		}
		if keyGVK != gvk {
			continue
		}
		if spec.Label != nil && !spec.Label.Matches(lbls) {
			return false
		}
		if spec.Field != nil && !spec.Field.Matches(flds) {
			return false
		}
		if spec.Namespaces != nil {
			cfg, ok := spec.Namespaces[obj.GetNamespace()]
			if !ok {
				return false
			}
			if cfg.LabelSelector != nil && !cfg.LabelSelector.Matches(lbls) {
				return false
			}
			if cfg.FieldSelector != nil && !cfg.FieldSelector.Matches(flds) {
				return false
			}
		}
		return true
	}
	return true // unscoped GVK: cached cluster-wide
}

// scopedClient simulates the manager's client with ManagerCacheOptions active:
// only objects the scoped cache would hold are seeded, so any read of an
// out-of-scope object fails with NotFound exactly like the real cache.
func scopedClient(t *testing.T, opts cache.Options, objs ...client.Object) client.Client {
	t.Helper()
	s := serviceScheme(t)
	visible := make([]client.Object, 0, len(objs))
	for _, o := range objs {
		if cacheVisible(t, opts, s, o) {
			visible = append(visible, o)
		}
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(visible...).Build()
}

// productionClaim creates a SandboxClaim exactly the way SandboxClaimer does
// in production (labels included), then stamps the bound-sandbox status
// agent-sandbox would report. Using the real create path is the point: the
// scope must cover what the controller actually writes, not test fixtures.
func productionClaim(t *testing.T, app *vibedv1.VibedApp, sandboxName string) *unstructured.Unstructured {
	t.Helper()
	seed := newFakeClient(t, app.DeepCopy(), sandboxTemplateObj("node-24", app.Namespace))
	if _, _, _, err := newSandboxClaimer(seed).EnsureClaim(context.Background(), app); err != nil {
		t.Fatalf("seed EnsureClaim: %v", err)
	}
	claim := newClaim()
	key := types.NamespacedName{Name: app.Name, Namespace: app.Namespace}
	if err := seed.Get(context.Background(), key, claim); err != nil {
		t.Fatalf("get seeded claim: %v", err)
	}
	if err := unstructured.SetNestedField(claim.Object, sandboxName, "status", "sandbox", "name"); err != nil {
		t.Fatalf("set claim status name: %v", err)
	}
	if err := unstructured.SetNestedStringSlice(claim.Object, []string{"10.1.2.3"}, "status", "sandbox", "podIPs"); err != nil {
		t.Fatalf("set claim status podIPs: %v", err)
	}
	claim.SetResourceVersion("") // reseedable into a fresh fake client
	return claim
}

// TestScopedCacheClaimReadPath proves the SandboxClaim label scope cannot hide
// the claims the controller itself creates: EnsureClaim's status read (and the
// SandboxTemplate fail-fast Get, which stays unscoped) still succeed through a
// client seeded with only cache-visible objects.
func TestScopedCacheClaimReadPath(t *testing.T) {
	opts := ManagerCacheOptions("vibed-pools")
	app := validApp("cache-claim")
	claim := productionClaim(t, app, "cache-claim-sbx")

	if !cacheVisible(t, opts, serviceScheme(t), claim) {
		t.Fatal("controller-created SandboxClaim must be inside the cache scope")
	}

	c := scopedClient(t, opts, app, sandboxTemplateObj("node-24", app.Namespace), claim)
	bound, ref, ip, err := (&SandboxClaimer{Client: c, PoolNamespace: "vibed-pools"}).EnsureClaim(context.Background(), app)
	if err != nil {
		t.Fatalf("EnsureClaim through scoped view: %v", err)
	}
	if !bound || ref != "cache-claim-sbx" || ip != "10.1.2.3" {
		t.Errorf("bound=%v ref=%q ip=%q, want bound with cache-claim-sbx/10.1.2.3", bound, ref, ip)
	}
}

// TestScopedCacheServiceReadPath proves the Service label scope cannot hide the
// controller's own read: EnsureService creates the per-app Service exactly as
// production does, then the rebind path (reconcileSelector's cached Get, plus
// boundPodSelector's claim Get) runs against the scoped view. A selector that
// excluded controller-created Services would surface here as NotFound.
func TestScopedCacheServiceReadPath(t *testing.T) {
	opts := ManagerCacheOptions("vibed-pools")
	app := validApp("cache-svc")

	// Create the Service through the production path.
	seed := fake.NewClientBuilder().WithScheme(serviceScheme(t)).
		WithObjects(app, productionClaim(t, app, "old-sandbox")).Build()
	if _, err := (&K8sServiceManager{Client: seed, AppPort: 8080}).EnsureService(context.Background(), app); err != nil {
		t.Fatalf("seed EnsureService: %v", err)
	}
	var svc corev1.Service
	key := types.NamespacedName{Name: ServiceName(app), Namespace: app.Namespace}
	if err := seed.Get(context.Background(), key, &svc); err != nil {
		t.Fatalf("get created service: %v", err)
	}
	if !cacheVisible(t, opts, serviceScheme(t), &svc) {
		t.Fatal("controller-created Service must be inside the cache scope")
	}
	svc.ResourceVersion = ""

	// Rebind: the claim now reports a new sandbox. EnsureService must find the
	// existing Service through the scoped view and repoint its selector.
	c := scopedClient(t, opts, app, productionClaim(t, app, "new-sandbox"), &svc)
	if _, err := (&K8sServiceManager{Client: c, AppPort: 8080}).EnsureService(context.Background(), app); err != nil {
		t.Fatalf("EnsureService through scoped view: %v", err)
	}
	var got corev1.Service
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get reconciled service: %v", err)
	}
	if want := sandboxNameHash("new-sandbox"); got.Spec.Selector[sandboxPodLabelKey] != want {
		t.Errorf("selector = %v, want repointed to %s=%s", got.Spec.Selector, sandboxPodLabelKey, want)
	}
}

// TestScopedCacheGateStrictReadPaths proves the Pod and ConfigMap scopes cover
// the strict gate's reads: LoadResult (the validation ConfigMap in the pool
// namespace) and CurrentImageID (warm-pool Pods selected on
// vibed.dev/template) both succeed through the scoped view.
func TestScopedCacheGateStrictReadPaths(t *testing.T) {
	const pool = "vibed-pools"
	const imageID = "docker-pullable://repo/image@sha256:cccccccccccc"
	opts := ManagerCacheOptions(pool)

	// Persist the result through the production writer, then extract the
	// ConfigMap it created.
	seed := fake.NewClientBuilder().WithScheme(serviceScheme(t)).Build()
	v := &templatevalidate.Validator{Client: seed, Namespace: pool}
	if err := v.Persist(context.Background(), []templatevalidate.Result{
		{Template: "node-24", Valid: true, ImageID: imageID},
	}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	var cm corev1.ConfigMap
	cmKey := types.NamespacedName{Name: templatevalidate.ConfigMapName, Namespace: pool}
	if err := seed.Get(context.Background(), cmKey, &cm); err != nil {
		t.Fatalf("get validation ConfigMap: %v", err)
	}
	cm.ResourceVersion = ""

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-24-pool-0",
			Namespace: pool,
			Labels:    map[string]string{templatevalidate.TemplateLabelKey: "node-24"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.1",
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{{ImageID: imageID}},
		},
	}

	c := scopedClient(t, opts, &cm, pod)
	g := &ConfigMapTemplateGate{Client: c, Namespace: pool, Strict: true}
	if ok, reason := g.Allowed(context.Background(), "node-24"); !ok {
		t.Errorf("strict gate denied through scoped view: %s", reason)
	}
}

// TestScopedCacheDropsUnmanagedObjects documents that the scope actually
// narrows: cluster objects vibeD never reads fall outside the cache, while
// VibedApps (unscoped, our own CRD) remain cluster-visible.
func TestScopedCacheDropsUnmanagedObjects(t *testing.T) {
	opts := ManagerCacheOptions("vibed-pools")
	s := serviceScheme(t)

	foreignClaim := newClaim()
	foreignClaim.SetName("not-ours")
	foreignClaim.SetNamespace("default")

	dropped := map[string]client.Object{
		"unlabeled Service": &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"},
		},
		"SandboxClaim without the app label": foreignClaim,
		"labeled Pod outside the pool namespace": &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p", Namespace: "default",
				Labels: map[string]string{templatevalidate.TemplateLabelKey: "node-24"},
			},
		},
		"unlabeled Pod in the pool namespace": &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "vibed-pools"},
		},
		"other ConfigMap in the pool namespace": &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: "vibed-pools"},
		},
		"validation-named ConfigMap elsewhere": &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: templatevalidate.ConfigMapName, Namespace: "default"},
		},
	}
	for name, obj := range dropped {
		if cacheVisible(t, opts, s, obj) {
			t.Errorf("%s should be outside the cache scope", name)
		}
	}

	if !cacheVisible(t, opts, s, validApp("any-app")) {
		t.Error("VibedApp must stay cluster-visible (unscoped GVK)")
	}
}

// TestManagerCacheOptionsEmptyPoolNamespace covers the guard for an empty
// -pool-namespace: "" is cache.AllNamespaces, so the builder must fall back to
// selector-only scoping rather than emit an empty-keyed Namespaces entry.
func TestManagerCacheOptionsEmptyPoolNamespace(t *testing.T) {
	opts := ManagerCacheOptions("")
	s := serviceScheme(t)

	labeledPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "p", Namespace: "anywhere",
		Labels: map[string]string{templatevalidate.TemplateLabelKey: "node-24"},
	}}
	if !cacheVisible(t, opts, s, labeledPod) {
		t.Error("labeled Pod should stay visible in any namespace when no pool namespace is set")
	}
	if cacheVisible(t, opts, s, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "anywhere"}}) {
		t.Error("unlabeled Pod should remain outside the scope")
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatevalidate.ConfigMapName, Namespace: "anywhere"}}
	if !cacheVisible(t, opts, s, cm) {
		t.Error("validation ConfigMap should stay visible in any namespace when no pool namespace is set")
	}
}

// TestLabelExists pins the EXISTS semantics the scopes rely on: any value
// matches, absence does not.
func TestLabelExists(t *testing.T) {
	sel := labelExists(appLabelKey)
	if !sel.Matches(labels.Set{appLabelKey: "some-app"}) {
		t.Error("selector must match any value of the key")
	}
	if !sel.Matches(labels.Set{appLabelKey: ""}) {
		t.Error("selector must match an empty value (EXISTS, not equality)")
	}
	if sel.Matches(labels.Set{"other": "x"}) {
		t.Error("selector must not match objects without the key")
	}
}
