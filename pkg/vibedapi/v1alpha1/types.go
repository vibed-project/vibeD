// Package v1alpha1 contains the VibedApp custom resource API.
//
// VibedApp is vibeD's single CRD; everything else reuses agent-sandbox CRDs
// (Sandbox, SandboxTemplate, SandboxClaim, SandboxWarmPool). The CRD shape is
// fixed by refactor.md §3 — keep this file and crds/vibedapp_crd.yaml in
// lockstep via `make manifests`.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Lane is the runtime track a VibedApp is scheduled on.
//
// +kubebuilder:validation:Enum=fast;general
type Lane string

const (
	// LaneFast is workerd/Spin: V8/WASM isolates for trusted-language workloads.
	LaneFast Lane = "fast"
	// LaneGeneral is Kata+Firecracker microVM isolation for arbitrary code.
	LaneGeneral Lane = "general"
)

// Phase is the high-level VibedApp lifecycle state.
//
// +kubebuilder:validation:Enum=Pending;Claiming;Starting;Ready;Suspended;Failed
type Phase string

const (
	PhasePending   Phase = "Pending"
	PhaseClaiming  Phase = "Claiming"
	PhaseStarting  Phase = "Starting"
	PhaseReady     Phase = "Ready"
	PhaseSuspended Phase = "Suspended"
	PhaseFailed    Phase = "Failed"
)

// EnvVar is a single environment variable passed to the user process.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// Source describes where the app's source tree comes from. Exactly one of
// TarballRef or GitRef is expected; the controller rejects specs that set
// both or neither.
type Source struct {
	// TarballRef is an S3 URL to the gzipped tarball of the app source.
	TarballRef string `json:"tarballRef,omitempty"`

	// GitRef is an optional git URL + commit used instead of a tarball.
	GitRef string `json:"gitRef,omitempty"`
}

// Runtime selects the lane and template, plus per-deploy runtime overrides.
type Runtime struct {
	// Lane is the runtime track; the classifier picks this and the user
	// override is rare.
	Lane Lane `json:"lane"`

	// Template names the prebaked template (e.g. node-24, python-313,
	// static-nginx). Empty falls back to the classifier's default for the
	// lane.
	Template string `json:"template,omitempty"`

	// Entrypoint overrides the autodetected start command in vibed-agent.
	Entrypoint string `json:"entrypoint,omitempty"`

	// Env are environment variables exposed to the user process.
	Env []EnvVar `json:"env,omitempty"`
}

// Resources mirrors the CPU/memory subset of corev1.ResourceRequirements that
// VibedApp exposes. Strings carry K8s quantity syntax ("500m", "256Mi") and
// are passed through verbatim.
type Resources struct {
	// +kubebuilder:default="500m"
	CPU string `json:"cpu,omitempty"`

	// +kubebuilder:default="256Mi"
	Memory string `json:"memory,omitempty"`
}

// VibedAppSpec is the desired state of a VibedApp.
type VibedAppSpec struct {
	// Owner is the authenticated user identity (email or OIDC subject) that
	// created the app. Used for AuthZ and per-user quota.
	Owner string `json:"owner"`

	// Source points to the app's code.
	Source Source `json:"source"`

	// Runtime selects the lane and template.
	Runtime Runtime `json:"runtime"`

	// Resources caps CPU/memory for the user process.
	Resources Resources `json:"resources,omitempty"`

	// TTL is the idle TTL before suspend-to-snapshot. Accepts Go duration
	// syntax ("30m", "1h"); validated by the controller.
	//
	// +kubebuilder:default="30m"
	TTL string `json:"ttl,omitempty"`
}

// VibedAppStatus is the observed state of a VibedApp.
type VibedAppStatus struct {
	// Phase is the coarse-grained lifecycle state.
	Phase Phase `json:"phase,omitempty"`

	// URL is the publicly resolvable URL of the running app, set once the
	// router has wired a route. Empty in Pending/Claiming/Starting.
	URL string `json:"url,omitempty"`

	// SandboxRef references the agent-sandbox Sandbox CR backing this app.
	SandboxRef string `json:"sandboxRef,omitempty"`

	// PodIP is the in-cluster IP of the bound Sandbox pod, populated once a
	// SandboxClaim has been bound. The controller uses this to reach
	// vibed-agent's control API (port 9000) and to inject source. Cleared on
	// Suspended. NOTE: a raw pod IP goes stale when the Sandbox pod is
	// recreated, so it is NOT the routing target — see RouteTarget.
	PodIP string `json:"podIP,omitempty"`

	// RouteTarget is the stable upstream ("host:port") vibed-router proxies
	// to. For general-lane apps it's the cluster-DNS name of the per-app
	// Service (which re-selects the bound pod across restarts, so it survives
	// pod-IP churn); for the workerd fast lane it's the worker's host:port.
	// Empty until Ready. The router falls back to PodIP when this is unset.
	RouteTarget string `json:"routeTarget,omitempty"`

	// LastDeployedAt is when the app last transitioned to Ready.
	LastDeployedAt *metav1.Time `json:"lastDeployedAt,omitempty"`

	// SnapshotRef is the S3 URL of the latest Firecracker snapshot. Empty
	// when no snapshot has been taken yet; populated on first suspend and
	// used to fast-path redeploys via snapshot-restore.
	SnapshotRef string `json:"snapshotRef,omitempty"`

	// Conditions reports detailed status using the standard metav1.Condition
	// shape, so consumers can match on Type/Status/Reason.
	//
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// VibedApp is a user-deployed application.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=vapp
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name=URL,type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name=Runtime,type=string,JSONPath=`.spec.runtime.template`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
type VibedApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VibedAppSpec   `json:"spec"`
	Status VibedAppStatus `json:"status,omitempty"`
}

// VibedAppList is a list of VibedApp.
//
// +kubebuilder:object:root=true
type VibedAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []VibedApp `json:"items"`
}
