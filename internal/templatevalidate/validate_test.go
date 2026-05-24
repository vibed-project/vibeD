package templatevalidate

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/runneragent"
)

var fixedNow = time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

func ok(lang, probeOut string) *runneragent.InfoResponse {
	return &runneragent.InfoResponse{
		Language:           lang,
		AgentContract:      runneragent.AgentContract,
		RuntimeProbe:       lang + " --version",
		RuntimeProbeOK:     true,
		RuntimeProbeOutput: probeOut,
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name      string
		slot      string
		info      *runneragent.InfoResponse
		probeErr  error
		wantValid bool
	}{
		{"node ok", "node-24", ok("node", "v24.1.0"), nil, true},
		{"static ok", "static-nginx", ok("static", "nginx/1.27"), nil, true},
		{"custom slot (no lang assertion)", "node-24-fips", ok("node", "v24"), nil, true},
		{"wrong language", "node-24", ok("python", "3.13"), nil, false},
		{"runtime probe failed", "node-24", &runneragent.InfoResponse{Language: "node", AgentContract: runneragent.AgentContract, RuntimeProbe: "node --version", RuntimeProbeOK: false, RuntimeProbeOutput: "not found"}, nil, false},
		{"no probe declared", "node-24", &runneragent.InfoResponse{Language: "node", AgentContract: runneragent.AgentContract}, nil, false},
		{"contract mismatch", "node-24", &runneragent.InfoResponse{Language: "node", AgentContract: "99", RuntimeProbe: "node --version", RuntimeProbeOK: true}, nil, false},
		{"agent unreachable", "node-24", nil, context.DeadlineExceeded, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := evaluate(tt.slot, "img:tag", tt.info, tt.probeErr, fixedNow)
			if r.Valid != tt.wantValid {
				t.Errorf("valid = %v, want %v (reason: %q)", r.Valid, tt.wantValid, r.Reason)
			}
			if !tt.wantValid && r.Reason == "" {
				t.Error("invalid result must carry a reason")
			}
			if tt.wantValid && r.Reason != "" {
				t.Errorf("valid result should have no reason, got %q", r.Reason)
			}
		})
	}
}

// fakeProbe returns canned /info per target IP.
type fakeProbe map[string]*runneragent.InfoResponse

func (f fakeProbe) Info(_ context.Context, target string) (*runneragent.InfoResponse, error) {
	ip := target
	if i := len(ip) - len(":9000"); i > 0 && ip[i:] == ":9000" {
		ip = ip[:i]
	}
	if info, ok := f[ip]; ok {
		return info, nil
	}
	return nil, context.DeadlineExceeded
}

func sandboxTemplate(name, image string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(SandboxTemplateGVK)
	u.SetName(name)
	u.SetNamespace("vibed-apps")
	_ = unstructured.SetNestedSlice(u.Object, []any{
		map[string]any{"name": "runtime", "image": image},
	}, "spec", "podTemplate", "spec", "containers")
	return u
}

func readyPod(name, slot, ip string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "vibed-apps", Labels: map[string]string{"vibed.dev/template": slot}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			PodIP:      ip,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func TestValidateAllAndPersist(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	objs := []client.Object{
		sandboxTemplate("node-24", "ghcr.io/x/node:1"),
		sandboxTemplate("python-313", "ghcr.io/x/py:1"),
		sandboxTemplate("go-123", "ghcr.io/x/go:1"), // no ready pod -> invalid
		readyPod("node-24-aaaa", "node-24", "10.0.0.1"),
		readyPod("python-313-bbbb", "python-313", "10.0.0.2"),
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	v := &Validator{
		Client:    c,
		Namespace: "vibed-apps",
		Now:       func() time.Time { return fixedNow },
		Probe: fakeProbe{
			"10.0.0.1": ok("node", "v24"),
			"10.0.0.2": ok("java", "openjdk"), // wrong language for python-313
		},
	}

	results, err := v.ValidateAll(context.Background())
	if err != nil {
		t.Fatalf("ValidateAll: %v", err)
	}
	got := map[string]Result{}
	for _, r := range results {
		got[r.Template] = r
	}
	if !got["node-24"].Valid {
		t.Errorf("node-24 should be valid: %q", got["node-24"].Reason)
	}
	if got["python-313"].Valid {
		t.Errorf("python-313 should be invalid (wrong language)")
	}
	if got["go-123"].Valid || got["go-123"].Reason == "" {
		t.Errorf("go-123 should be invalid with a reason (no ready pod), got %+v", got["go-123"])
	}

	// Persist + reload round-trip.
	if err := v.Persist(context.Background(), results); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	r, found, err := LoadResult(context.Background(), c, "vibed-apps", "node-24")
	if err != nil || !found {
		t.Fatalf("LoadResult node-24: found=%v err=%v", found, err)
	}
	if !r.Valid || r.Image != "ghcr.io/x/node:1" {
		t.Errorf("loaded node-24 = %+v", r)
	}
}
