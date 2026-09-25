package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ClusterName = "cluster"

	PhaseIdle         = "Idle"
	PhasePreflight    = "Preflight"
	PhaseBackup       = "Backup"
	PhasePreload      = "Preload"
	PhaseControlPlane = "ControlPlane"
	PhaseComponents   = "Components"
	PhaseWorkers      = "Workers"
	PhaseVerify       = "Verify"
	PhaseFailed       = "Failed"

	UpgradeActionResume = "resume"
	UpgradeActionAbort  = "abort"

	ReasonUpgrading     = "Upgrading"
	ReasonBlocked       = "Blocked"
	ReasonAborted       = "Aborted"
	ReasonRestoring     = "Restoring"
	ReasonRestoreManual = "RestoreManual"
	ReasonPhaseFailed   = "PhaseFailed"
	ReasonUpgraded      = "Upgraded"

	ConditionAvailable           = "Available"
	ConditionUpgradeBlocked      = "UpgradeBlocked"
	ConditionStorageReady        = "StorageReady"
	ConditionVirtualizationReady = "VirtualizationReady"
	ConditionPlatformReady       = "PlatformReady"
)

type APISpec struct {
	VIP string `json:"vip"`
	// +kubebuilder:validation:Enum=arp;bgp
	// +kubebuilder:default=arp
	VIPMode string `json:"vipMode,omitempty"`
}

type RegistrySpec struct {
	// +optional
	Mirror string `json:"mirror,omitempty"`
}

type KMSSpec struct {
	Endpoint  string `json:"endpoint"`
	SecretRef string `json:"secretRef"`
}

type EncryptionSpec struct {
	// +optional
	KMS *KMSSpec `json:"kms,omitempty"`
}

type UpgradeSpec struct {
	// +kubebuilder:validation:Enum="";resume;abort
	// +optional
	Action string `json:"action,omitempty"`
}

type ClusterSpec struct {
	DesiredVersion string  `json:"desiredVersion"`
	API            APISpec `json:"api"`
	// +optional
	MaintenanceWindow string `json:"maintenanceWindow,omitempty"`
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	NodeConcurrency int32 `json:"nodeConcurrency,omitempty"`
	// +optional
	Registry RegistrySpec `json:"registry,omitempty"`
	// +optional
	Encryption EncryptionSpec `json:"encryption,omitempty"`
	// +optional
	Upgrade UpgradeSpec `json:"upgrade,omitempty"`
}

type ComponentStatus struct {
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	Available bool   `json:"available"`
	Degraded  bool   `json:"degraded"`
	Message   string `json:"message,omitempty"`
}

type BackupRecord struct {
	Time     metav1.Time `json:"time"`
	Location string      `json:"location"`
	Digest   string      `json:"digest"`
}

type UpgradeStatus struct {
	From           string      `json:"from"`
	To             string      `json:"to"`
	StartedAt      metav1.Time `json:"startedAt"`
	PhaseStartedAt metav1.Time `json:"phaseStartedAt"`
	// +kubebuilder:validation:Minimum=1
	Attempt int32 `json:"attempt"`
	// +optional
	Backup string `json:"backup,omitempty"`
	// +optional
	FailedPhase string `json:"failedPhase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

type ClusterStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Components []ComponentStatus `json:"components,omitempty"`
	// +optional
	Backups []BackupRecord `json:"backups,omitempty"`
	// +optional
	Upgrade *UpgradeStatus `json:"upgrade,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Desired",type=string,JSONPath=`.spec.desiredVersion`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type Cluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterSpec   `json:"spec,omitempty"`
	Status            ClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Cluster{}, &ClusterList{})
}
