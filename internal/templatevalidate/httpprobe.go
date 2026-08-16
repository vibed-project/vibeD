package templatevalidate

import (
	"context"
	"time"

	"github.com/vibed-project/vibeD/internal/runneragent"
)

// HTTPInfoProbe is the production InfoProbe: it calls GET /info on a warm
// pod's in-cluster control API (target = "podIP:9000"). /info is
// unauthenticated, but the token is sent when set (harmless).
//
// Timeout bounds each probe call. The validator loop hands us the manager's
// long-lived context, so without a per-call deadline a wedged pod would stall
// the whole validation pass. Zero means the 15s default — generous, since the
// agent caps its runtime probe at 10s and a healthy /info answers well within
// that.
type HTTPInfoProbe struct {
	Token   string
	Timeout time.Duration
}

func (p HTTPInfoProbe) Info(ctx context.Context, target string) (*runneragent.InfoResponse, error) {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runneragent.NewClient("http://"+target, p.Token).Info(ictx)
}
