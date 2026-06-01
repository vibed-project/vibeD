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

// K8sResolver resolves via the VibedApp list — status.podIP records the bound
// sandbox pod's IP. Backed by a cached client (controller-runtime manager), so
// the List is served from a watched local cache, not a live API call per
// request.
type K8sResolver struct {
	Client    client.Client
	Namespace string
}

func (r *K8sResolver) AllowedFor(ctx context.Context, srcIP string) ([]string, bool) {
	if srcIP == "" {
		return nil, false
	}
	var list vibedv1.VibedAppList
	if err := r.Client.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		return nil, false
	}
	for i := range list.Items {
		if list.Items[i].Status.PodIP == srcIP {
			return list.Items[i].Spec.Egress.AllowedHosts, true
		}
	}
	return nil, false
}
