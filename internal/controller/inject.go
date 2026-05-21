package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/vibed-project/vibeD/internal/runneragent"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// TemplateEntrypoint is the per-template launcher every runner image ships at
// a fixed path (see templates/*/entrypoint.sh). The agent runs it after
// extracting the source, so the controller doesn't need to know each
// template's start command — it just hands the agent this path.
const TemplateEntrypoint = "/etc/vibed/entrypoint.sh"

// HTTPInjector is the production Injector. It POSTs the VibedApp's source
// (spec.source.tarballRef) to the bound sandbox's vibed-agent so the agent
// pulls the tarball into /workspace and starts the user process.
type HTTPInjector struct {
	Token   string
	Timeout time.Duration
}

// NewHTTPInjector returns an injector with a sensible default timeout. The
// timeout is generous because /inject does a synchronous tarball download +
// process start inside the sandbox.
func NewHTTPInjector(token string) *HTTPInjector {
	return &HTTPInjector{Token: token, Timeout: 60 * time.Second}
}

func (i *HTTPInjector) Inject(ctx context.Context, target string, app *vibedv1.VibedApp) (bool, error) {
	if app.Spec.Source.TarballRef == "" {
		return false, fmt.Errorf("inject: spec.source.tarballRef is empty (only tarball sources are supported)")
	}
	timeout := i.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Command: an explicit spec.runtime.entrypoint wins (run via sh -c);
	// otherwise the template's own /etc/vibed/entrypoint.sh decides how to
	// start the process for that runtime.
	var command []string
	if ep := app.Spec.Runtime.Entrypoint; ep != "" {
		command = []string{"sh", "-c", ep}
	} else {
		command = []string{TemplateEntrypoint}
	}

	env := map[string]string{}
	for _, e := range app.Spec.Runtime.Env {
		env[e.Name] = e.Value
	}

	c := runneragent.NewClient("http://"+target, i.Token)
	status, err := c.Inject(ictx, runneragent.InjectRequest{
		SourceURL: app.Spec.Source.TarballRef,
		Command:   command,
		Env:       env,
	})
	if err != nil {
		return false, err
	}
	return status.State == runneragent.StateRunning, nil
}

// compile-time check.
var _ Injector = (*HTTPInjector)(nil)
