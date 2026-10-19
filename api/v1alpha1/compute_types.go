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
	// +kubebuilder:validation:XValidation:rule="self >= oldSelf",message="rootDisk.sizeGi can only grow"
	SizeGi int32 `json:"sizeGi"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="rootDisk.storageClass is immutable"
	// +optional
	StorageClass string `json:"storageClass,omitempty"`
}

type VMDisk struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	VolumeRef string `json:"volumeRef"`
	// +kubebuilder:validation:Enum=virtio;sata;scsi
	// +optional
	Bus string `json:"bus,omitempty"`
	// +kubebuilder:validation:MaxLength=36
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._-]+$`
	// +optional
	Serial string `json:"serial,omitempty"`
}

type VMCDROM struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	ImageRef string `json:"imageRef"`
}

type VMNetwork struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	SubnetRef string `json:"subnetRef"`
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	// +listType=set
	SecurityGroups []string `json:"securityGroups,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	ElasticIPRef string `json:"elasticIPRef,omitempty"`
	// +kubebuilder:validation:Pattern=`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`
	// +optional
	MACAddress string `json:"macAddress,omitempty"`
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:validation:XValidation:rule="isIP(self) && ip(self).family() == 4",message="address must be an IPv4 address"
	// +optional
	Address string `json:"address,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.secretRef) || (!has(self.userData) && !has(self.networkData))",message="secretRef excludes userData and networkData"
type VMCloudInit struct {
	// +kubebuilder:validation:MaxLength=65536
	// +optional
	UserData string `json:"userData,omitempty"`
	// +kubebuilder:validation:MaxLength=65536
	// +optional
	NetworkData string `json:"networkData,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

type VMHostDevice struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([-A-Za-z0-9._/]*[A-Za-z0-9])?$`
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
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	// +optional
	UUID string `json:"uuid,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._-]+$`
	// +optional
	Serial string `json:"serial,omitempty"`
}

type VMCPU struct {
	// +optional
	Dedicated bool `json:"dedicated,omitempty"`
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._-]{1,64}$`
	// +optional
	Model string `json:"model,omitempty"`
}

type VMScheduling struct {
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.image) || self.image.name.matches('^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$')",message="image.name must be a DNS subdomain"
// +kubebuilder:validation:XValidation:rule="(has(self.firmware) && has(self.firmware.uuid)) == (has(oldSelf.firmware) && has(oldSelf.firmware.uuid)) && (!(has(self.firmware) && has(self.firmware.uuid)) || self.firmware.uuid == oldSelf.firmware.uuid)",message="firmware.uuid is immutable"
// +kubebuilder:validation:XValidation:rule="(has(self.firmware) && has(self.firmware.serial)) == (has(oldSelf.firmware) && has(oldSelf.firmware.serial)) && (!(has(self.firmware) && has(self.firmware.serial)) || self.firmware.serial == oldSelf.firmware.serial)",message="firmware.serial is immutable"
type VirtualMachineSpec struct {
	// +kubebuilder:validation:MinLength=1
	InstanceType string `json:"instanceType"`
	// +optional
	Preference string `json:"preference,omitempty"`
	// +optional
	Image    *ImageRef  `json:"image,omitempty"`
	RootDisk VMRootDisk `json:"rootDisk"`
	// +kubebuilder:validation:MaxItems=24
	// +kubebuilder:validation:XValidation:rule="self.all(a, self.exists_one(b, b.volumeRef == a.volumeRef))",message="a volume can be listed once"
	// +optional
	// +listType=atomic
	Disks []VMDisk `json:"disks,omitempty"`
	// +kubebuilder:validation:MaxItems=4
	// +optional
	// +listType=atomic
	CDROMs []VMCDROM `json:"cdroms,omitempty"`
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:XValidation:rule="self.all(a, self.exists_one(b, b.subnetRef == a.subnetRef))",message="a subnet can be attached once"
	// +kubebuilder:validation:XValidation:rule="self.all(n, oldSelf.all(o, o.subnetRef != n.subnetRef || (has(o.address) == has(n.address) && (!has(n.address) || n.address == o.address))))",message="address is immutable"
	// +optional
	// +listType=atomic
	Networks []VMNetwork `json:"networks,omitempty"`
	// +optional
	CloudInit *VMCloudInit `json:"cloudInit,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	// +listType=set
	SSHKeys []string `json:"sshKeys,omitempty"`
	// +kubebuilder:default=true
	// +optional
	Running bool `json:"running"`
	// +kubebuilder:validation:Enum=LiveMigrate;LiveMigrateIfPossible;None
	// +optional
	EvictionStrategy string `json:"evictionStrategy,omitempty"`
	// +kubebuilder:validation:MaxItems=8
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

type VMMigration struct {
	Name  string `json:"name"`
	Phase string `json:"phase"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
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
	// +optional
	Migration *VMMigration `json:"migration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 52",message="the name must have at most 52 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
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
// +kubebuilder:validation:XValidation:rule="!has(self.image) || self.image.name.matches('^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$')",message="image.name must be a DNS subdomain"
type VolumeDataSource struct {
	// +optional
	Image *ImageRef `json:"image,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	SnapshotRef string `json:"snapshotRef,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
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
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="volumeRef is immutable"
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
	// +kubebuilder:validation:Pattern=`^[^\r\n]+$`
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
