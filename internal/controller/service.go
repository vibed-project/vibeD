package controller

import (
	"context"
	"fmt"
	"hash/fnv"
	"maps"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// sandboxPodLabelKey is the label agent-sandbox stamps on every Sandbox pod,
// keyed to an FNV-1a hash of the Sandbox name. agent-sandbox v0.5 replaced the
// older per-claim UID label (agents.x-k8s.io/claim-uid) with this scheme, so
// the per-app Service selects on it. If a future agent-sandbox release renames
// the label or changes the hash, this and sandboxNameHash are the two places to
// update.
const sandboxPodLabelKey = "agents.x-k8s.io/sandbox-name-hash"

// ServiceNamePrefix namespaces the per-app Services the controller creates.
// Matches vibed-router's Caddy @id prefix so a Service and its route share a
// name — easy to correlate when debugging.
const ServiceNamePrefix = "vibed-app-"

// ServiceName is the per-app Service name. Built from AppLabel (12 lowercase
// alphanumerics) so it's stable, ≤63 chars regardless of the user's app name,
// and always starts with a letter (RFC 1035, which Service names require —
// raw VibedApp names may start with a digit).
func ServiceName(app *vibedv1.VibedApp) string {
	return ServiceNamePrefix + AppLabel(app)
}

// ServiceManager ensures the stable network identity a Ready app routes
// through. EnsureService returns the routable upstream ("host:port"); an
// empty string means "no managed Service" (the reconciler then leaves
// RouteTarget unset and the router falls back to the pod IP).
type ServiceManager interface {
	EnsureService(ctx context.Context, app *vibedv1.VibedApp) (target string, err error)
}

// K8sServiceManager ensures one ClusterIP Service per general-lane VibedApp,
// selecting the bound Sandbox pod via the label selector agent-sandbox
// publishes on the claim's status.sandbox.selector. The Service gives the
// router a stable upstream (its cluster-DNS name) that survives Sandbox pod
// restarts — a raw pod IP goes stale when the pod is recreated with a new IP,
// leaving Caddy pointed at a dead address (502).
//
// agent-sandbox owns the pod label scheme (v0.5+ tracks pods by
// agents.x-k8s.io/sandbox-name-hash, replacing the older per-claim label), so
// vibeD reads whatever selector the controller reports rather than hard-coding
// a label key that can change across agent-sandbox releases.
type K8sServiceManager struct {
	Client client.Client
	// AppPort is the user-process port every template exposes (8080).
	AppPort int
}

// EnsureService creates (idempotently) the per-app Service and returns the
// stable routable target "host:port" the router should proxy to. It reads the
// SandboxClaim (same name/namespace as the VibedApp) for the pod label selector
// agent-sandbox assigns the bound Sandbox; the claim is guaranteed to exist by
// the Starting phase, where this runs.
func (m *K8sServiceManager) EnsureService(ctx context.Context, app *vibedv1.VibedApp) (string, error) {
	port := m.AppPort
	if port == 0 {
		port = 8080
	}

	selector, err := m.boundPodSelector(ctx, app)
	if err != nil {
		return "", err
	}

	name := ServiceName(app)
	target := fmt.Sprintf("%s.%s.svc.cluster.local:%d", name, app.Namespace, port)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
			Labels:    map[string]string{"vibed.dev/app": app.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         vibedv1.GroupVersion.String(),
				Kind:               "VibedApp",
				Name:               app.Name,
				UID:                app.UID,
				Controller:         ptrBool(true),
				BlockOwnerDeletion: ptrBool(true),
			}},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       int32(port),
				TargetPort: intstr.FromInt(port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}

	if err := m.Client.Create(ctx, svc); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("create Service %s/%s: %w", app.Namespace, name, err)
		}
		// Already exists — a redeploy can rebind to a new Sandbox (new
		// selector), so make sure the selector still points at the live pod.
		if err := m.reconcileSelector(ctx, app, name, selector); err != nil {
			return "", err
		}
	}
	return target, nil
}

// reconcileSelector patches an existing per-app Service's selector when the app
// has rebound to a different Sandbox (a new selector) since the Service was
// made.
func (m *K8sServiceManager) reconcileSelector(ctx context.Context, app *vibedv1.VibedApp, name string, selector map[string]string) error {
	var existing corev1.Service
	key := types.NamespacedName{Name: name, Namespace: app.Namespace}
	if err := m.Client.Get(ctx, key, &existing); err != nil {
		return fmt.Errorf("get Service %s/%s: %w", app.Namespace, name, err)
	}
	if maps.Equal(existing.Spec.Selector, selector) {
		return nil
	}
	existing.Spec.Selector = selector
	if err := m.Client.Update(ctx, &existing); err != nil {
		return fmt.Errorf("update Service selector %s/%s: %w", app.Namespace, name, err)
	}
	return nil
}

// boundPodSelector builds the label selector matching the app's bound Sandbox
// pod. agent-sandbox reports the adopted Sandbox name on the claim's
// status.sandbox.name and labels that Sandbox's pod sandboxPodLabelKey =
// NameHash(name); we reproduce that hash so the per-app Service tracks the pod
// across restarts. The claim is guaranteed to exist by the Starting phase,
// where this runs; an unset sandbox name means the bind hasn't been reported
// yet, which the caller treats as a retryable error.
func (m *K8sServiceManager) boundPodSelector(ctx context.Context, app *vibedv1.VibedApp) (map[string]string, error) {
	claim := newClaim()
	key := types.NamespacedName{Name: app.Name, Namespace: app.Namespace}
	if err := m.Client.Get(ctx, key, claim); err != nil {
		return nil, fmt.Errorf("get SandboxClaim %s/%s for service selector: %w", app.Namespace, app.Name, err)
	}
	name, found, err := unstructured.NestedString(claim.Object, "status", "sandbox", "name")
	if err != nil {
		return nil, fmt.Errorf("read SandboxClaim %s/%s status.sandbox.name: %w", app.Namespace, app.Name, err)
	}
	if !found || name == "" {
		return nil, fmt.Errorf("SandboxClaim %s/%s has no bound sandbox yet", app.Namespace, app.Name)
	}
	return map[string]string{sandboxPodLabelKey: sandboxNameHash(name)}, nil
}

// sandboxNameHash reproduces agent-sandbox's controllers.NameHash: an FNV-1a
// 32-bit hash of the Sandbox name rendered as 8 hex digits. It's the value of
// the sandboxPodLabelKey label agent-sandbox puts on that Sandbox's pod.
func sandboxNameHash(sandboxName string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sandboxName))
	return fmt.Sprintf("%08x", h.Sum32())
}

// DummyServiceManager makes no Service and reports no managed target, so the
// router falls back to the pod IP. Used in tests and as the reconciler's
// default until cmd/vibed-controller wires the real K8sServiceManager.
type DummyServiceManager struct{}

func (DummyServiceManager) EnsureService(context.Context, *vibedv1.VibedApp) (string, error) {
	return "", nil
}
