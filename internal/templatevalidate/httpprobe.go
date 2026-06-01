package templatevalidate

import (
	"context"

	"github.com/vibed-project/vibeD/internal/runneragent"
)

// HTTPInfoProbe is the production InfoProbe: it calls GET /info on a warm
// pod's in-cluster control API (target = "podIP:9000"). /info is
// unauthenticated, but the token is sent when set (harmless).
type HTTPInfoProbe struct{ Token string }

func (p HTTPInfoProbe) Info(ctx context.Context, target string) (*runneragent.InfoResponse, error) {
	return runneragent.NewClient("http://"+target, p.Token).Info(ctx)
}
