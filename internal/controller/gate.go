package controller

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/templatevalidate"
)

// TemplateGate decides whether an app may claim a sandbox of a given template,
// based on bring-your-own base-image validation. The default ConfigMapTemplateGate
// hard-gates only on a *recorded failure*: a slot that hasn't been validated yet
// is allowed (the periodic validator catches a bad image shortly, and a truly
// broken image still fails at probe/inject) — so the gate never blocks deploys
// during the validation warm-up window, only known-bad images.
//
// With Strict=true the gate flips to fail-closed and additionally compares the
// live warm-pool pod's resolved ImageID against the validated ImageID, so a
// mutable-tag content swap forces re-validation before any new claim binds.
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
	// Strict flips the warmup-window default from fail-open to fail-closed:
	// a slot with no recorded result is denied, and a Valid result is only
	// honored when the live warm-pool pod's resolved ImageID still matches
	// the validated ImageID. Use this for production clusters where deploys
	// must not race ahead of validation.
	Strict bool
}

func (g *ConfigMapTemplateGate) Allowed(ctx context.Context, template string) (bool, string) {
	res, found, err := templatevalidate.LoadResult(ctx, g.Client, g.Namespace, template)
	if err != nil {
		if g.Strict {
			return false, fmt.Sprintf("template validation ConfigMap unreadable: %v", err)
		}
		return true, "" // ConfigMap absent at startup — don't block in default mode
	}
	if !found {
		if g.Strict {
			return false, "template not yet validated (strict mode); the validator runs on a 2-minute interval"
		}
		return true, "" // warm-up window — allow in default mode
	}
	if !res.Valid {
		return false, res.Reason
	}
	if g.Strict {
		// Belt-and-braces: even when the recorded result is Valid, a mutable
		// tag may have been re-pushed since validation. First reject results
		// that don't carry an ImageID at all (legacy records or warm pod
		// hadn't pulled yet at validate-time), then verify the live warm
		// pod's resolved ImageID still matches what the validator approved.
		if res.ImageID == "" {
			return false, "validation result has no recorded ImageID — awaiting re-validation (strict mode)"
		}
		liveID, ok := templatevalidate.CurrentImageID(ctx, g.Client, g.Namespace, template)
		if !ok {
			return false, "no Ready warm-pool pod to check image identity against (strict mode)"
		}
		if liveID != res.ImageID {
			return false, fmt.Sprintf("warm-pool pod image has changed since validation (was %s, now %s) — awaiting re-validation", shortID(res.ImageID), shortID(liveID))
		}
	}
	return true, ""
}

// shortID trims a kubelet-style ImageID to its last 12 digest chars for
// human-readable diagnostics in deny reasons.
func shortID(id string) string {
	if i := len(id); i > 12 {
		return "…" + id[i-12:]
	}
	return id
}
