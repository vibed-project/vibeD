// Package helm runs `helm template` against the vibeD chart with various
// value overrides and asserts that the hardening knobs the v0.4.0 review
// landed are still present in the rendered manifests. These tests don't
// touch Go code — they catch chart refactors that accidentally strip
// securityContext fields, DNS lockdown rules, the Squid TTL config, or any
// of the new gate/audit/log flags.
//
// Skips cleanly when `helm` isn't on PATH (e.g. minimal CI images) so a
// missing binary doesn't fail the whole test suite.
package helm

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// chartPath returns the absolute path to the vibeD chart, derived from this
// test file's location so it works regardless of where `go test` is invoked.
func chartPath(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("can't resolve caller for chart path")
	}
	// test/helm/chart_test.go → ../../deploy/helm/vibed
	return filepath.Join(filepath.Dir(here), "..", "..", "deploy", "helm", "vibed")
}

// helmTemplate renders the chart with the given --set overrides and returns
// the stdout. Skips the test (not fails) when `helm` isn't installed.
func helmTemplate(t *testing.T, sets ...string) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skipf("helm not on PATH; skipping chart render test")
	}
	args := []string{"template", chartPath(t)}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(helm, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm template failed: %v\nstderr:\n%s", err, stderr.String())
	}
	return stdout.String()
}

// containsAll asserts every want string is in render; reports the first miss
// with enough context for someone to fix the chart.
func containsAll(t *testing.T, label, render string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(render, w) {
			t.Errorf("[%s] rendered chart is missing %q\n(first 200 chars of render: %.200s)", label, w, render)
		}
	}
}

// TestEgressProxyHardening pins the Pod + container securityContext fields
// on the egress-proxy Deployment. A Helm refactor that drops any of these
// turns the egress chokepoint back into a root-with-writable-FS process.
func TestEgressProxyHardening(t *testing.T) {
	r := helmTemplate(t, "egressControl.enabled=true")
	containsAll(t, "egress-proxy securityContext", r,
		"runAsNonRoot: true",
		"runAsUser: 13",
		"fsGroup: 13",
		"readOnlyRootFilesystem: true",
		"capabilities: { drop: [\"ALL\"] }",
		"seccompProfile: { type: RuntimeDefault }",
	)
}

// TestSquidConfigHasRebindingMitigation pins the DNS TTL clamps that close
// the rebinding window between authz approval and the CONNECT lookup.
func TestSquidConfigHasRebindingMitigation(t *testing.T) {
	r := helmTemplate(t, "egressControl.enabled=true")
	containsAll(t, "Squid DNS TTL", r,
		"positive_dns_ttl 1 hour",
		"negative_dns_ttl 5 minutes",
	)
}

// TestSandboxNetworkPolicyDNSLockdown pins the DNS egress selector that
// stops sandboxes from tunneling data via 8.8.8.8 or an attacker-controlled
// resolver. The cluster admin can override the selector, but the default
// must lock to CoreDNS in kube-system.
func TestSandboxNetworkPolicyDNSLockdown(t *testing.T) {
	r := helmTemplate(t, "networkPolicy.enabled=true")
	containsAll(t, "sandbox NP DNS selector", r,
		"kubernetes.io/metadata.name: kube-system",
		"k8s-app: kube-dns",
	)
	// The egress section must include the kube-dns selector together with
	// the :53 ports — i.e. there must be no rule allowing :53 without a `to:`
	// block. We check by searching for the legacy unrestricted form.
	bad := "- ports:\n        - { protocol: UDP, port: 53 }\n        - { protocol: TCP, port: 53 }"
	if strings.Contains(r, bad) {
		t.Errorf("sandbox NetworkPolicy still has unrestricted :53 egress (no `to:` selector). DNS tunneling is wide open. Found:\n%s", bad)
	}
}

// TestControllerStrictFlagWiring pins the --template-validation-strict CLI
// flag that flips the BYO gate from fail-open to fail-closed.
func TestControllerStrictFlagWiring(t *testing.T) {
	r := helmTemplate(t, "templateValidation.strict=true")
	containsAll(t, "controller strict flag", r,
		"--template-validation-strict=true",
	)
}

// TestAuditFailClosedWiring pins the audit failClosed field in the config
// ConfigMap so a refactor doesn't drop it silently.
func TestAuditFailClosedWiring(t *testing.T) {
	r := helmTemplate(t, "config.audit.failClosed=true")
	containsAll(t, "audit.failClosed", r,
		"failClosed: true",
	)
}

// TestLogStreamCapWiring pins the per-user log stream cap config key.
func TestLogStreamCapWiring(t *testing.T) {
	r := helmTemplate(t, "config.limits.maxConcurrentLogStreamsPerUser=7")
	containsAll(t, "log stream cap", r,
		"maxConcurrentLogStreamsPerUser: 7",
	)
}

// TestDefaultsAreSafe spot-checks that a fresh `helm template` (no overrides)
// keeps the defaults the security review committed to: BYO validation on,
// egress proxy off (governance opt-in), log cap at 10.
func TestDefaultsAreSafe(t *testing.T) {
	r := helmTemplate(t)
	containsAll(t, "default values", r,
		"--validate-images=true",
		"--template-validation-strict=false",
		"maxConcurrentLogStreamsPerUser: 10",
		"failClosed: false",
	)
}
