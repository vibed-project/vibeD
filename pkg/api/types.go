package api

import (
	"fmt"
	"time"
)

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
	SecretRefs map[string]string `json:"secret_refs,omitempty"` // env var name → "secret-name:key"
	Language   string            `json:"language,omitempty"`
	StaticFiles string           `json:"static_files,omitempty"` // ConfigMap name for static content (skip build)
	Error       string           `json:"error,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	StorageRef  string           `json:"storage_ref,omitempty"`
	Version     int              `json:"version"`                    // Current version number (1-based, 0 = pre-versioning)
	VersionID   string           `json:"version_id,omitempty"`       // Unique ID for this version snapshot
	SharedWith  []string         `json:"shared_with,omitempty"`      // UserIDs with read-only access
}

// ArtifactSummary is a lightweight view of an artifact for list responses.
type ArtifactSummary struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	OwnerID    string           `json:"owner_id,omitempty"`
	Status     ArtifactStatus   `json:"status"`
	Target     DeploymentTarget `json:"target"`
	URL        string           `json:"url,omitempty"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
	Version    int              `json:"version"`
	SharedWith []string         `json:"shared_with,omitempty"`
}

// ArtifactVersion represents a point-in-time snapshot of an artifact.
type ArtifactVersion struct {
	VersionID  string            `json:"version_id"`
	ArtifactID string            `json:"artifact_id"`
	Version    int               `json:"version"`
	ImageRef   string            `json:"image_ref"`
	StorageRef string            `json:"storage_ref,omitempty"`
	EnvVars    map[string]string `json:"env_vars,omitempty"`
	SecretRefs map[string]string `json:"secret_refs,omitempty"`
	Status     ArtifactStatus    `json:"status"`
	URL        string            `json:"url,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	CreatedBy  string            `json:"created_by"`
}

// ErrVersionNotFound is returned when a specific version does not exist.
type ErrVersionNotFound struct {
	ArtifactID string
	Version    int
}

func (e *ErrVersionNotFound) Error() string {
	return fmt.Sprintf("version %d of artifact %q not found", e.Version, e.ArtifactID)
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
		ID:         a.ID,
		Name:       a.Name,
		OwnerID:    a.OwnerID,
		Status:     a.Status,
		Target:     a.Target,
		URL:        a.URL,
		CreatedAt:  a.CreatedAt,
		UpdatedAt:  a.UpdatedAt,
		Version:    a.Version,
		SharedWith: a.SharedWith,
	}
}
