package egressauthz

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// Resolver maps a source pod IP to the egress allow-list of the VibedApp whose
// bound sandbox has that IP. found=false means the IP isn't a known sandbox
// (then only system hosts are allowed).
type Resolver interface {
	AllowedFor(ctx context.Context, srcIP string) (hosts []string, found bool)
}

// PodIPIndexField is the cache index mapping a VibedApp to its bound sandbox
// pod IP (status.podIP). The egress binary registers it on its manager (and
// tests on the fake client) via IndexVibedAppPodIP so AllowedFor is an indexed
// map hit instead of a full-list scan.
const PodIPIndexField = "status.podIP"

// IndexVibedAppPodIP is the PodIPIndexField extractor. Apps without a bound
// pod IP are not indexed.
func IndexVibedAppPodIP(obj client.Object) []string {
	app, ok := obj.(*vibedv1.VibedApp)
	if !ok || app.Status.PodIP == "" {
		return nil
	}
	return []string{app.Status.PodIP}
}

// K8sResolver resolves via the VibedApp list — status.podIP records the bound
// sandbox pod's IP. Backed by a cached client (controller-runtime manager), so
// the List is served from a watched local cache, not a live API call per
// request. The client MUST have PodIPIndexField registered.
type K8sResolver struct {
	Client    client.Client
	Namespace string
}

func (r *K8sResolver) AllowedFor(ctx context.Context, srcIP string) ([]string, bool) {
	if srcIP == "" {
		return nil, false
	}
	var list vibedv1.VibedAppList
	if err := r.Client.List(ctx, &list, client.InNamespace(r.Namespace), client.MatchingFields{PodIPIndexField: srcIP}); err != nil {
		return nil, false
	}
	if len(list.Items) == 0 {
		return nil, false
	}
	// As before, the first listed match wins (an IP bound to two apps is a
	// transient state at most).
	return list.Items[0].Spec.Egress.AllowedHosts, true
}
