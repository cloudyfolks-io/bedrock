package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	BackupPhasePending    = "Pending"
	BackupPhaseInProgress = "InProgress"
	BackupPhaseCompleted  = "Completed"
	BackupPhaseFailed     = "Failed"

	RestorePhasePending    = "Pending"
	RestorePhaseInProgress = "InProgress"
	RestorePhaseCompleted  = "Completed"
	RestorePhaseFailed     = "Failed"
)

type VirtualMachineBackupSpec struct {
	// +kubebuilder:validation:MinLength=1
	VMRef string `json:"vmRef"`
	// +kubebuilder:validation:MinLength=1
	Target string `json:"target"`
	// +optional
	Schedule string `json:"schedule,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	Keep int32 `json:"keep,omitempty"`
}

type VMBackupRecord struct {
	// +kubebuilder:validation:MinLength=1
	ID   string      `json:"id"`
	Time metav1.Time `json:"time"`
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}

type VirtualMachineBackupStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	LastBackup *metav1.Time `json:"lastBackup,omitempty"`
	// +optional
	// +listType=atomic
	Backups []VMBackupRecord `json:"backups,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="VM",type=string,JSONPath=`.spec.vmRef`
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type VirtualMachineBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VirtualMachineBackupSpec   `json:"spec"`
	Status            VirtualMachineBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VirtualMachineBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMachineBackup `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="has(self.targetName) != (has(self.replace) && self.replace)",message="exactly one of targetName and replace must be set"
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="a restore is immutable"
type VirtualMachineRestoreSpec struct {
	// +kubebuilder:validation:MinLength=1
	BackupRef string `json:"backupRef"`
	// +optional
	BackupID string `json:"backupID,omitempty"`
	// +optional
	TargetName string `json:"targetName,omitempty"`
	// +optional
	Replace bool `json:"replace,omitempty"`
}

type VirtualMachineRestoreStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	Phase string `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Backup",type=string,JSONPath=`.spec.backupRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type VirtualMachineRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VirtualMachineRestoreSpec   `json:"spec"`
	Status            VirtualMachineRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VirtualMachineRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMachineRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VirtualMachineBackup{}, &VirtualMachineBackupList{}, &VirtualMachineRestore{}, &VirtualMachineRestoreList{})
}
