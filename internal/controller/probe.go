package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/vibed-project/vibeD/internal/runneragent"
)

// HTTPAgentProbe is the production AgentProbe. It calls vibed-agent's
// /healthz on the bound pod's port-9000 control API to report readiness.
//
// Token: agent installs typically share a fixed bearer token across the
// fleet (see config.FastPathConfig.AgentToken). We pass it through on
// every probe so the agent's auth middleware doesn't reject us.
//
// Timeout: kept aggressive so a wedged pod can't stall a reconciler.
// Failures bubble up as err and the reconciler requeues.
type HTTPAgentProbe struct {
	Token   string
	Timeout time.Duration
}

// NewHTTPAgentProbe returns a probe with a sensible default timeout.
func NewHTTPAgentProbe(token string) *HTTPAgentProbe {
	return &HTTPAgentProbe{Token: token, Timeout: 2 * time.Second}
}

// IsReady probes vibed-agent at target ("host:port"). target should always
// be the bound pod's IP + the agent's control port (9000 by template
// convention).
func (p *HTTPAgentProbe) IsReady(ctx context.Context, target string) (bool, error) {
	if target == "" {
		return false, fmt.Errorf("HTTPAgentProbe: target is empty")
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := runneragent.NewClient("http://"+target, p.Token)
	if err := c.Healthz(probeCtx); err != nil {
		return false, err
	}
	return true, nil
}

// Compile-time check that the type satisfies the interface.
var _ AgentProbe = (*HTTPAgentProbe)(nil)

// silence linter when net/http is the only import added for a future
// override — currently unused but keeps the file's intent obvious.
var _ = http.DefaultClient
