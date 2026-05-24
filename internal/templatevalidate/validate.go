// Package templatevalidate validates the base images backing vibeD's warm
// pools — the core of the bring-your-own base-image feature. For each template
// slot it reads a running warm-pool pod's GET /info and asserts the image (a)
// embeds a working vibed-agent, (b) declares the language vibeD routes to that
// slot, and (c) actually has that language runtime (the runtime probe passed).
//
// Results are written to a ConfigMap the controller's claim path reads, so a
// misconfigured BYO image fails deploys fast with a clear reason instead of
// silently breaking apps.
package templatevalidate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vibed-project/vibeD/internal/runneragent"
)

// ConfigMapName is the ConfigMap (in the apps namespace) where validation
// results are stored, keyed by template slot → JSON Result.
const ConfigMapName = "vibed-template-validation"

// SandboxTemplateGVK identifies the agent-sandbox SandboxTemplate.
var SandboxTemplateGVK = schema.GroupVersionKind{
	Group:   "extensions.agents.x-k8s.io",
	Version: "v1alpha1",
	Kind:    "SandboxTemplate",
}

// slotLanguage is the language vibeD's classifier routes to each built-in
// slot. A BYO image registered for a slot must declare (and actually have)
// this language. Slots not listed here (custom names) skip the language-match
// assertion — the agent + runtime-probe checks still apply.
var slotLanguage = map[string]string{
	"static-nginx": "static",
	"node-24":      "node",
	"python-313":   "python",
	"go-123":       "go",
	"base-al2023":  "base",
}

// ExpectedLanguage returns the language a slot must serve, and whether the slot
// is a known built-in (false → no language assertion).
func ExpectedLanguage(slot string) (string, bool) {
	l, ok := slotLanguage[slot]
	return l, ok
}

// Result is the validation outcome for one template slot.
type Result struct {
	Template  string `json:"template"`
	Image     string `json:"image"`
	Language  string `json:"language,omitempty"` // language the agent reported
	Valid     bool   `json:"valid"`
	Reason    string `json:"reason,omitempty"`
	CheckedAt string `json:"checkedAt"`
}

// InfoProbe fetches GET /info from a warm pod's control API at target
// ("podIP:9000"). The controller wires the HTTP implementation; tests fake it.
type InfoProbe interface {
	Info(ctx context.Context, target string) (*runneragent.InfoResponse, error)
}

// Validator validates every SandboxTemplate in a namespace against the
// bring-your-own contract.
type Validator struct {
	Client    client.Client
	Namespace string
	Probe     InfoProbe
	AppPort   int // agent control port; defaults to 9000
	Now       func() time.Time
}

// evaluate is the pure validation decision for one slot. Factored out so the
// language-match + runtime-probe logic is unit-tested without a cluster.
//
//   - probeErr != nil           → invalid (agent unreachable / no /info)
//   - expected known & mismatch → invalid (wrong-language image)
//   - !RuntimeProbeOK            → invalid (runtime missing)
//   - no VIBED_RUNTIME_PROBE     → invalid (image doesn't meet the contract)
func evaluate(slot, image string, info *runneragent.InfoResponse, probeErr error, now time.Time) Result {
	r := Result{Template: slot, Image: image, CheckedAt: now.UTC().Format(time.RFC3339)}
	if probeErr != nil {
		r.Reason = fmt.Sprintf("agent /info unreachable (image may not embed vibed-agent or failed to start): %v", probeErr)
		return r
	}
	r.Language = info.Language
	if info.AgentContract != runneragent.AgentContract {
		r.Reason = fmt.Sprintf("agent contract %q does not match vibeD's %q", info.AgentContract, runneragent.AgentContract)
		return r
	}
	if want, known := ExpectedLanguage(slot); known && info.Language != want {
		r.Reason = fmt.Sprintf("expected language %q for slot %q but image declares %q", want, slot, info.Language)
		return r
	}
	if info.RuntimeProbe == "" {
		r.Reason = "image declares no VIBED_RUNTIME_PROBE (cannot prove the language runtime is present)"
		return r
	}
	if !info.RuntimeProbeOK {
		r.Reason = fmt.Sprintf("runtime probe %q failed: %s", info.RuntimeProbe, info.RuntimeProbeOutput)
		return r
	}
	r.Valid = true
	return r
}

// ValidateAll validates every SandboxTemplate in the namespace and returns one
// Result per slot. It does not create pods: it reads a Ready warm-pool pod per
// slot (the exact image the pool runs). A slot with no Ready pod is invalid —
// a broken image shows up as a pool that never becomes Ready.
func (v *Validator) ValidateAll(ctx context.Context) ([]Result, error) {
	if v.Now == nil {
		v.Now = time.Now
	}
	port := v.AppPort
	if port == 0 {
		port = 9000
	}

	tmpls := &unstructured.UnstructuredList{}
	tmpls.SetGroupVersionKind(SandboxTemplateGVK)
	if err := v.Client.List(ctx, tmpls, client.InNamespace(v.Namespace)); err != nil {
		return nil, fmt.Errorf("list SandboxTemplates: %w", err)
	}

	results := make([]Result, 0, len(tmpls.Items))
	for i := range tmpls.Items {
		t := &tmpls.Items[i]
		slot := t.GetName()
		image := firstContainerImage(t.Object)

		podIP, ok := v.readyPodIP(ctx, slot)
		if !ok {
			results = append(results, Result{
				Template:  slot,
				Image:     image,
				CheckedAt: v.Now().UTC().Format(time.RFC3339),
				Reason:    "no Ready warm-pool pod for this slot (image may be failing to start — check the pod)",
			})
			continue
		}
		info, err := v.Probe.Info(ctx, fmt.Sprintf("%s:%d", podIP, port))
		results = append(results, evaluate(slot, image, info, err, v.Now()))
	}
	return results, nil
}

// readyPodIP finds a Running, Ready warm-pool pod for the slot and returns its
// IP. The warm pods carry the vibed.dev/template label (set by warmpools.yaml).
func (v *Validator) readyPodIP(ctx context.Context, slot string) (string, bool) {
	pods := &corev1.PodList{}
	if err := v.Client.List(ctx, pods,
		client.InNamespace(v.Namespace),
		client.MatchingLabels{"vibed.dev/template": slot},
	); err != nil {
		return "", false
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning || p.Status.PodIP == "" || p.DeletionTimestamp != nil {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				return p.Status.PodIP, true
			}
		}
	}
	return "", false
}

// firstContainerImage digs spec.podTemplate.spec.containers[0].image out of an
// unstructured SandboxTemplate.
func firstContainerImage(obj map[string]any) string {
	containers, found, err := unstructured.NestedSlice(obj, "spec", "podTemplate", "spec", "containers")
	if err != nil || !found || len(containers) == 0 {
		return ""
	}
	c, ok := containers[0].(map[string]any)
	if !ok {
		return ""
	}
	img, _ := c["image"].(string)
	return img
}

// Persist writes the results to the validation ConfigMap (created if absent).
func (v *Validator) Persist(ctx context.Context, results []Result) error {
	data := make(map[string]string, len(results))
	for _, r := range results {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		data[r.Template] = string(b)
	}
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: v.Namespace, Name: ConfigMapName}
	err := v.Client.Get(ctx, key, cm)
	if err != nil {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName, Namespace: v.Namespace},
			Data:       data,
		}
		return v.Client.Create(ctx, cm)
	}
	cm.Data = data
	return v.Client.Update(ctx, cm)
}

// LoadAll reads every slot's validation result from the ConfigMap (for the
// `vibed validate-images` status report). Returns an empty slice when the
// ConfigMap exists but is empty; an error only when it can't be read.
func LoadAll(ctx context.Context, c client.Client, namespace string) ([]Result, error) {
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ConfigMapName}, cm); err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(cm.Data))
	for _, raw := range cm.Data {
		var r Result
		if json.Unmarshal([]byte(raw), &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// LoadResult reads one slot's validation result from the ConfigMap. found is
// false when the slot hasn't been validated yet (treated as "not yet known").
func LoadResult(ctx context.Context, c client.Client, namespace, slot string) (Result, bool, error) {
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ConfigMapName}, cm); err != nil {
		return Result{}, false, err
	}
	raw, ok := cm.Data[slot]
	if !ok {
		return Result{}, false, nil
	}
	var r Result
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return Result{}, false, err
	}
	return r, true, nil
}
