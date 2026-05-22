package environment

import (
	"log/slog"

	"github.com/vibed-project/vibeD/internal/k8s"
	"github.com/vibed-project/vibeD/pkg/api"

	"k8s.io/client-go/discovery"
)

// Detector probes the Kubernetes cluster to determine which deployment
// targets are available.
type Detector struct {
	discovery discovery.DiscoveryInterface
	logger    *slog.Logger
}

// NewDetector creates a new environment detector.
func NewDetector(clients *k8s.Clients, logger *slog.Logger) *Detector {
	return &Detector{
		discovery: clients.Discovery,
		logger:    logger,
	}
}

// DetectResult holds the availability of each deployment target.
type DetectResult struct {
	Sandbox bool
	// Kubernetes is always available if we can talk to the cluster.
	Kubernetes bool
}

// Detect checks which deployment targets are available in the cluster.
func (d *Detector) Detect() *DetectResult {
	result := &DetectResult{
		Kubernetes: true, // Always available if we have a client.
	}

	sandbox, err := k8s.HasCRD(d.discovery, "agents.x-k8s.io", "v1alpha1", "sandboxes")
	if err != nil {
		d.logger.Warn("failed to check for Sandbox CRDs", "error", err)
	}
	result.Sandbox = sandbox

	d.logger.Debug("detected deployment targets",
		"sandbox", result.Sandbox,
		"kubernetes", result.Kubernetes,
	)

	return result
}

// SelectTarget chooses the best available deployment target.
func (d *Detector) SelectTarget(preferred api.DeploymentTarget) (api.DeploymentTarget, error) {
	result := d.Detect()

	switch preferred {
	case api.TargetSandbox:
		if !result.Sandbox {
			return "", &api.ErrTargetUnavailable{Target: api.TargetSandbox}
		}
		return api.TargetSandbox, nil
	case api.TargetKubernetes:
		return api.TargetKubernetes, nil

	default: // "auto"
		if result.Sandbox {
			return api.TargetSandbox, nil
		}
		return api.TargetKubernetes, nil
	}
}

// ListTargets returns info about all deployment targets.
func (d *Detector) ListTargets() []api.TargetInfo {
	result := d.Detect()

	return []api.TargetInfo{
		{
			Name:        api.TargetSandbox,
			Available:   result.Sandbox,
			Preferred:   true,
			Description: "Agent Sandbox - stateful, isolated workload environment",
		},
		{
			Name:        api.TargetKubernetes,
			Available:   result.Kubernetes,
			Preferred:   false,
			Description: "Plain Kubernetes - Deployment + Service (always available)",
		},
	}
}
