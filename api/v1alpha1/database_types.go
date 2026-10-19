package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DatabasePhaseProvisioning = "Provisioning"
	DatabasePhaseReady        = "Ready"
	DatabasePhaseUnhealthy    = "Unhealthy"
	DatabasePhaseSuspended    = "Suspended"
	DatabasePhaseRestoring    = "Restoring"
	DatabasePhaseUpgrading    = "Upgrading"
	DatabasePhaseDeleting     = "Deleting"
	DatabasePhaseFailed       = "Failed"

	DatabaseFieldManager = "bedrock-database"

	AnnotationResetPassword = "bedrock.cloudyfolks.io/reset-password"
)

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="restore is immutable"
type DatabaseRestore struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	SourceUID string `json:"sourceUID"`
	// +optional
	TargetTime *metav1.Time `json:"targetTime,omitempty"`
}

type DatabaseBackupSpec struct {
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
}

// +kubebuilder:validation:XValidation:rule="self.engine != 'valkey' || !self.backup.enabled",message="a Valkey database has no backups: set backup.enabled to false"
// +kubebuilder:validation:XValidation:rule="has(self.restore) == has(oldSelf.restore)",message="restore is immutable"
type DatabaseSpec struct {
	// +kubebuilder:validation:Enum=postgresql;mysql;valkey
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="engine is immutable"
	Engine string `json:"engine"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	Version string `json:"version"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Class string `json:"class"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100000
	// +kubebuilder:validation:XValidation:rule="self >= oldSelf",message="storageGi cannot shrink"
	StorageGi int32 `json:"storageGi"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="subnetRef is immutable"
	// +kubebuilder:default=default
	// +optional
	SubnetRef string `json:"subnetRef,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=43
	// +kubebuilder:validation:items:XValidation:rule="isCIDR(self) && cidr(self).ip().family() == 4",message="allowedCIDRs entries must be IPv4 CIDRs"
	// +optional
	// +listType=set
	AllowedCIDRs []string `json:"allowedCIDRs,omitempty"`
	// +kubebuilder:default={enabled: true}
	Backup DatabaseBackupSpec `json:"backup"`
	// +optional
	Restore *DatabaseRestore `json:"restore,omitempty"`
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

type DatabaseAddress struct {
	// +kubebuilder:validation:MaxLength=63
	Subnet string `json:"subnet"`
	// +kubebuilder:validation:MaxLength=15
	IP string `json:"ip"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

type DatabaseBackupItem struct {
	// +kubebuilder:validation:MaxLength=253
	ID string `json:"id"`
	// +kubebuilder:validation:MaxLength=63
	Phase string `json:"phase"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

type DatabaseBackupStatus struct {
	// +optional
	LastAt *metav1.Time `json:"lastAt,omitempty"`
	// +optional
	PITRFrom *metav1.Time `json:"pitrFrom,omitempty"`
	// +optional
	PITRTo *metav1.Time `json:"pitrTo,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	// +kubebuilder:validation:MaxItems=100
	// +optional
	// +listType=atomic
	Items []DatabaseBackupItem `json:"items,omitempty"`
}

type DatabaseStats struct {
	// +kubebuilder:validation:Minimum=0
	// +optional
	DiskUsedBytes int64 `json:"diskUsedBytes,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Connections int32 `json:"connections,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	ReplicationLagSeconds int32 `json:"replicationLagSeconds,omitempty"`
	// +optional
	SampledAt *metav1.Time `json:"sampledAt,omitempty"`
}

type DatabaseStatus struct {
	CommonStatus `json:",inline"`
	// +kubebuilder:validation:Enum=Provisioning;Ready;Unhealthy;Suspended;Restoring;Upgrading;Deleting;Failed
	// +optional
	Phase string `json:"phase,omitempty"`
	// +kubebuilder:validation:MaxItems=8
	// +optional
	// +listType=atomic
	Addresses []DatabaseAddress `json:"addresses,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	// +optional
	Instances int32 `json:"instances,omitempty"`
	// +optional
	Backup *DatabaseBackupStatus `json:"backup,omitempty"`
	// +optional
	Stats *DatabaseStats `json:"stats,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 52",message="the name must have at most 52 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
// +kubebuilder:printcolumn:name="Engine",type=string,JSONPath=`.spec.engine`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.class`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.addresses[0].ip`
type Database struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DatabaseSpec   `json:"spec"`
	Status            DatabaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Database `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Database{}, &DatabaseList{})
}
