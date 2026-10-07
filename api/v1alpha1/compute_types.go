package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DiskBusVirtio = "virtio"
	DiskBusSATA   = "sata"
	DiskBusSCSI   = "scsi"

	EvictionStrategyLiveMigrate           = "LiveMigrate"
	EvictionStrategyLiveMigrateIfPossible = "LiveMigrateIfPossible"
	EvictionStrategyNone                  = "None"

	VMPhaseStopped   = "Stopped"
	VMPhaseStarting  = "Starting"
	VMPhaseRunning   = "Running"
	VMPhaseMigrating = "Migrating"
	VMPhaseStopping  = "Stopping"
	VMPhaseFailed    = "Failed"

	VolumePhasePending   = "Pending"
	VolumePhaseAvailable = "Available"
	VolumePhaseInUse     = "InUse"
	VolumePhaseResizing  = "Resizing"
	VolumePhaseFailed    = "Failed"

	VolumeSnapshotPhasePending = "Pending"
	VolumeSnapshotPhaseReady   = "Ready"
	VolumeSnapshotPhaseFailed  = "Failed"
)

type VMRootDisk struct {
	// +kubebuilder:validation:Minimum=1
	SizeGi int32 `json:"sizeGi"`
	// +optional
	StorageClass string `json:"storageClass,omitempty"`
}

type VMDisk struct {
	// +kubebuilder:validation:MinLength=1
	VolumeRef string `json:"volumeRef"`
	// +kubebuilder:validation:Enum=virtio;sata;scsi
	// +kubebuilder:default=virtio
	// +optional
	Bus string `json:"bus,omitempty"`
}

type VMCDROM struct {
	// +kubebuilder:validation:MinLength=1
	ImageRef string `json:"imageRef"`
}

type VMNetwork struct {
	// +kubebuilder:validation:MinLength=1
	SubnetRef string `json:"subnetRef"`
	// +optional
	// +listType=set
	SecurityGroups []string `json:"securityGroups,omitempty"`
	// +optional
	ElasticIPRef string `json:"elasticIPRef,omitempty"`
	// +kubebuilder:validation:Pattern=`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`
	// +optional
	MACAddress string `json:"macAddress,omitempty"`
}

type VMCloudInit struct {
	// +optional
	UserData string `json:"userData,omitempty"`
	// +optional
	NetworkData string `json:"networkData,omitempty"`
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

type VMHostDevice struct {
	// +kubebuilder:validation:MinLength=1
	ClaimRef string `json:"claimRef"`
}

// +kubebuilder:validation:XValidation:rule="!(has(self.secureBoot) && self.secureBoot) || (has(self.uefi) && self.uefi)",message="secureBoot requires uefi"
type VMFirmware struct {
	// +optional
	UEFI bool `json:"uefi,omitempty"`
	// +optional
	SecureBoot bool `json:"secureBoot,omitempty"`
	// +optional
	TPM bool `json:"tpm,omitempty"`
}

type VMCPU struct {
	// +optional
	Dedicated bool `json:"dedicated,omitempty"`
	// +optional
	Model string `json:"model,omitempty"`
}

type VMScheduling struct {
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
}

type VirtualMachineSpec struct {
	// +kubebuilder:validation:MinLength=1
	InstanceType string `json:"instanceType"`
	// +optional
	Preference string `json:"preference,omitempty"`
	// +optional
	Image    *ImageRef  `json:"image,omitempty"`
	RootDisk VMRootDisk `json:"rootDisk"`
	// +optional
	// +listType=atomic
	Disks []VMDisk `json:"disks,omitempty"`
	// +optional
	// +listType=atomic
	CDROMs []VMCDROM `json:"cdroms,omitempty"`
	// +optional
	// +listType=atomic
	Networks []VMNetwork `json:"networks,omitempty"`
	// +optional
	CloudInit *VMCloudInit `json:"cloudInit,omitempty"`
	// +optional
	// +listType=set
	SSHKeys []string `json:"sshKeys,omitempty"`
	// +kubebuilder:default=true
	// +optional
	Running bool `json:"running"`
	// +kubebuilder:validation:Enum=LiveMigrate;LiveMigrateIfPossible;None
	// +optional
	EvictionStrategy string `json:"evictionStrategy,omitempty"`
	// +optional
	// +listType=atomic
	HostDevices []VMHostDevice `json:"hostDevices,omitempty"`
	// +optional
	Firmware *VMFirmware `json:"firmware,omitempty"`
	// +optional
	CPU *VMCPU `json:"cpu,omitempty"`
	// +optional
	Scheduling *VMScheduling `json:"scheduling,omitempty"`
}

type VMAddress struct {
	Subnet string `json:"subnet"`
	IP     string `json:"ip"`
	// +optional
	MAC string `json:"mac,omitempty"`
}

type VMGuestAgent struct {
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	OS string `json:"os,omitempty"`
	// +optional
	Hostname string `json:"hostname,omitempty"`
}

type VMConsole struct {
	// +optional
	VNCPath string `json:"vncPath,omitempty"`
	// +optional
	SerialPath string `json:"serialPath,omitempty"`
}

type VirtualMachineStatus struct {
	CommonStatus `json:",inline"`
	// +kubebuilder:validation:Enum=Stopped;Starting;Running;Migrating;Stopping;Failed
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Node string `json:"node,omitempty"`
	// +optional
	// +listType=atomic
	Addresses []VMAddress `json:"addresses,omitempty"`
	// +optional
	ElasticIP string `json:"elasticIP,omitempty"`
	// +optional
	GuestAgent *VMGuestAgent `json:"guestAgent,omitempty"`
	// +optional
	Console *VMConsole `json:"console,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.instanceType`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.status.node`
type VirtualMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VirtualMachineSpec   `json:"spec"`
	Status            VirtualMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VirtualMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMachine `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="[has(self.image), has(self.snapshotRef), has(self.volumeRef)].filter(set, set).size() <= 1",message="at most one source must be set"
type VolumeDataSource struct {
	// +optional
	Image *ImageRef `json:"image,omitempty"`
	// +optional
	SnapshotRef string `json:"snapshotRef,omitempty"`
	// +optional
	VolumeRef string `json:"volumeRef,omitempty"`
}

type VolumeSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self >= oldSelf",message="sizeGi can only grow"
	SizeGi int32 `json:"sizeGi"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="storageClass is immutable"
	// +optional
	StorageClass string `json:"storageClass,omitempty"`
	// +optional
	Source *VolumeDataSource `json:"source,omitempty"`
	// +kubebuilder:validation:Enum=virtio;sata;scsi
	// +kubebuilder:default=virtio
	// +optional
	Bus string `json:"bus,omitempty"`
}

type VolumeStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	AttachedTo string `json:"attachedTo,omitempty"`
	// +optional
	ActualSizeGi int32 `json:"actualSizeGi,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Size",type=integer,JSONPath=`.spec.sizeGi`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Attached To",type=string,JSONPath=`.status.attachedTo`
type Volume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VolumeSpec   `json:"spec"`
	Status            VolumeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VolumeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Volume `json:"items"`
}

type VolumeSnapshotSpec struct {
	// +kubebuilder:validation:MinLength=1
	VolumeRef string `json:"volumeRef"`
	// +optional
	Retention *metav1.Duration `json:"retention,omitempty"`
}

type VolumeSnapshotStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Volume",type=string,JSONPath=`.spec.volumeRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type VolumeSnapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VolumeSnapshotSpec   `json:"spec"`
	Status            VolumeSnapshotStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VolumeSnapshotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VolumeSnapshot `json:"items"`
}

type SSHKeySpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=16384
	PublicKey string `json:"publicKey"`
}

type SSHKeyStatus struct {
	CommonStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
type SSHKey struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SSHKeySpec   `json:"spec"`
	Status            SSHKeyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SSHKeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SSHKey `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VirtualMachine{}, &VirtualMachineList{}, &Volume{}, &VolumeList{}, &VolumeSnapshot{}, &VolumeSnapshotList{}, &SSHKey{}, &SSHKeyList{})
}
