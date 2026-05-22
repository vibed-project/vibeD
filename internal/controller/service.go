package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// ClaimUIDLabel is the label agent-sandbox stamps on a bound Sandbox pod,
// carrying the UID of the SandboxClaim that bound it. It's the only
// per-app-unique label on the pod, so it's what the per-app Service selects
// on. Verified against agent-sandbox v0.4.5: a bound pod's
// metadata.labels["agents.x-k8s.io/claim-uid"] == its SandboxClaim's UID.
const ClaimUIDLabel = "agents.x-k8s.io/claim-uid"

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
// selecting the bound Sandbox pod by its claim-uid label. The Service gives
// the router a stable upstream (its cluster-DNS name) that survives Sandbox
// pod restarts — a raw pod IP goes stale when the pod is recreated with a new
// IP, leaving Caddy pointed at a dead address (502).
type K8sServiceManager struct {
	Client client.Client
	// AppPort is the user-process port every template exposes (8080).
	AppPort int
}

// EnsureService creates (idempotently) the per-app Service and returns the
// stable routable target "host:port" the router should proxy to. It reads the
// SandboxClaim (same name/namespace as the VibedApp) for the claim UID that
// keys the pod selector; the claim is guaranteed to exist by the Starting
// phase, where this runs.
func (m *K8sServiceManager) EnsureService(ctx context.Context, app *vibedv1.VibedApp) (string, error) {
	port := m.AppPort
	if port == 0 {
		port = 8080
	}

	claimUID, err := m.claimUID(ctx, app)
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
			Selector: map[string]string{ClaimUIDLabel: claimUID},
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
		// Already exists — a redeploy can rebind to a new claim/pod, so make
		// sure the selector still points at the current claim.
		if err := m.reconcileSelector(ctx, app, name, claimUID); err != nil {
			return "", err
		}
	}
	return target, nil
}

// reconcileSelector patches an existing per-app Service's selector when the
// app has rebound to a different claim (new UID) since the Service was made.
func (m *K8sServiceManager) reconcileSelector(ctx context.Context, app *vibedv1.VibedApp, name, claimUID string) error {
	var existing corev1.Service
	key := types.NamespacedName{Name: name, Namespace: app.Namespace}
	if err := m.Client.Get(ctx, key, &existing); err != nil {
		return fmt.Errorf("get Service %s/%s: %w", app.Namespace, name, err)
	}
	if existing.Spec.Selector[ClaimUIDLabel] == claimUID {
		return nil
	}
	existing.Spec.Selector = map[string]string{ClaimUIDLabel: claimUID}
	if err := m.Client.Update(ctx, &existing); err != nil {
		return fmt.Errorf("update Service selector %s/%s: %w", app.Namespace, name, err)
	}
	return nil
}

// claimUID reads the bound SandboxClaim's UID — the value agent-sandbox stamps
// onto the pod via ClaimUIDLabel.
func (m *K8sServiceManager) claimUID(ctx context.Context, app *vibedv1.VibedApp) (string, error) {
	claim := newClaim()
	key := types.NamespacedName{Name: app.Name, Namespace: app.Namespace}
	if err := m.Client.Get(ctx, key, claim); err != nil {
		return "", fmt.Errorf("get SandboxClaim %s/%s for service selector: %w", app.Namespace, app.Name, err)
	}
	uid := string(claim.GetUID())
	if uid == "" {
		return "", fmt.Errorf("SandboxClaim %s/%s has no UID yet", app.Namespace, app.Name)
	}
	return uid, nil
}

// DummyServiceManager makes no Service and reports no managed target, so the
// router falls back to the pod IP. Used in tests and as the reconciler's
// default until cmd/vibed-controller wires the real K8sServiceManager.
type DummyServiceManager struct{}

func (DummyServiceManager) EnsureService(context.Context, *vibedv1.VibedApp) (string, error) {
	return "", nil
}
