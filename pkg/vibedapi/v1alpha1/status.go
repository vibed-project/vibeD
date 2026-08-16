package v1alpha1

import "github.com/vibed-project/vibeD/pkg/api"

// StatusFromPhase maps a VibedApp lifecycle phase onto the artifact-status
// display vocabulary the /v1 API, MCP tools, and SSE event bridge all speak.
// It lives here — beside the Phase constants — so those producers share one
// source of truth and their phase→status translations cannot drift apart.
func StatusFromPhase(p Phase) api.ArtifactStatus {
	switch p {
	case PhasePending, PhaseClaiming:
		return api.StatusPending
	case PhaseStarting:
		return api.StatusDeploying
	case PhaseReady:
		return api.StatusRunning
	case PhaseSuspended:
		return api.StatusRunning
	case PhaseFailed:
		return api.StatusFailed
	default:
		return api.StatusPending
	}
}
