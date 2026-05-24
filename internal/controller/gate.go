package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/templatevalidate"
)

// TemplateGate decides whether an app may claim a sandbox of a given template,
// based on bring-your-own base-image validation. It hard-gates only on a
// *recorded failure*: a slot that hasn't been validated yet is allowed (the
// periodic validator catches a bad image shortly, and a truly broken image
// still fails at probe/inject) — so the gate never blocks deploys during the
// validation warm-up window, only known-bad images.
type TemplateGate interface {
	Allowed(ctx context.Context, template string) (allowed bool, reason string)
}

// AllowAllTemplateGate is the default (no validation wired) — used in tests and
// when validation is disabled.
type AllowAllTemplateGate struct{}

func (AllowAllTemplateGate) Allowed(context.Context, string) (bool, string) { return true, "" }

// ConfigMapTemplateGate reads the validator's results from the
// vibed-template-validation ConfigMap.
type ConfigMapTemplateGate struct {
	Client    client.Client
	Namespace string
}

func (g *ConfigMapTemplateGate) Allowed(ctx context.Context, template string) (bool, string) {
	res, found, err := templatevalidate.LoadResult(ctx, g.Client, g.Namespace, template)
	if err != nil || !found {
		return true, "" // not yet validated (or ConfigMap absent) — don't block
	}
	if res.Valid {
		return true, ""
	}
	return false, res.Reason
}
