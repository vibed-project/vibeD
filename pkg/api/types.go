package api

import "time"

// DeploymentTarget represents a supported deployment backend.
type DeploymentTarget string

const (
	TargetAuto       DeploymentTarget = "auto"
	TargetKnative    DeploymentTarget = "knative"
	TargetKubernetes DeploymentTarget = "kubernetes"
	TargetWasmCloud  DeploymentTarget = "wasmcloud"
)

// ArtifactStatus represents the lifecycle state of a deployed artifact.
type ArtifactStatus string

const (
	StatusPending   ArtifactStatus = "pending"
	StatusBuilding  ArtifactStatus = "building"
	StatusDeploying ArtifactStatus = "deploying"
	StatusRunning   ArtifactStatus = "running"
	StatusFailed    ArtifactStatus = "failed"
	StatusDeleted   ArtifactStatus = "deleted"
)

// Artifact is the central domain object representing a deployed workload.
type Artifact struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	OwnerID    string            `json:"owner_id,omitempty"`
	Status     ArtifactStatus    `json:"status"`
	Target     DeploymentTarget  `json:"target"`
	ImageRef   string            `json:"image_ref,omitempty"`
	URL        string            `json:"url,omitempty"`
	Port       int               `json:"port,omitempty"`
	EnvVars    map[string]string `json:"env_vars,omitempty"`
	Language   string            `json:"language,omitempty"`
	Error      string            `json:"error,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	StorageRef string            `json:"storage_ref,omitempty"`
}

// ArtifactSummary is a lightweight view of an artifact for list responses.
type ArtifactSummary struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	OwnerID   string           `json:"owner_id,omitempty"`
	Status    ArtifactStatus   `json:"status"`
	Target    DeploymentTarget `json:"target"`
	URL       string           `json:"url,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// TargetInfo describes the availability of a deployment target.
type TargetInfo struct {
	Name        DeploymentTarget `json:"name"`
	Available   bool             `json:"available"`
	Preferred   bool             `json:"preferred"`
	Description string           `json:"description"`
}

// ToSummary converts an Artifact to an ArtifactSummary.
func (a *Artifact) ToSummary() ArtifactSummary {
	return ArtifactSummary{
		ID:        a.ID,
		Name:      a.Name,
		OwnerID:   a.OwnerID,
		Status:    a.Status,
		Target:    a.Target,
		URL:       a.URL,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
}
