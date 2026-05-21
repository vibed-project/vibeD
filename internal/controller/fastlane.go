package controller

import (
	"context"

	"github.com/vibed-project/vibeD/internal/workerd"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// WorkerdFastLane adapts a workerd.Client to the FastLaneDeployer interface
// the reconciler consumes. It deploys the app's source (via tarballRef) to
// the loader replica the app shards to and returns the reachable host:port.
type WorkerdFastLane struct {
	Client *workerd.Client
}

func (w WorkerdFastLane) Deploy(ctx context.Context, app *vibedv1.VibedApp) (string, error) {
	target, err := w.Client.Deploy(ctx, app.Name, app.Spec.Source.TarballRef, app.Spec.Runtime.Entrypoint)
	if err != nil {
		return "", err
	}
	return target.Addr(), nil
}

func (w WorkerdFastLane) Remove(ctx context.Context, app *vibedv1.VibedApp) error {
	return w.Client.Remove(ctx, app.Name)
}

// compile-time check.
var _ FastLaneDeployer = WorkerdFastLane{}
