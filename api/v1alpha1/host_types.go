package v1alpha1

import (
	"slices"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	RoleControlPlane  = "control-plane"
	RoleCephOSD       = "ceph-osd"
	RoleFabricGateway = "fabric-gateway"
	RoleWorkload      = "workload"

	ConditionManagementApplied = "ManagementApplied"
	ConditionMaintenanceMode   = "MaintenanceMode"
	ConditionRebootPending     = "RebootPending"
)

const (
	LabelManaged         = "bedrock.cloudyfolks.io/managed"
	AgentFieldManager    = "bedrock-agent"
	OperatorFieldManager = "bedrock-operator"
)

func AllRoles() []string {
	return []string{RoleControlPlane, RoleCephOSD, RoleFabricGateway, RoleWorkload}
}

func ValidRole(role string) bool {
	return slices.Contains(AllRoles(), role)
}

type ManagementSpec struct {
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`
}

type HostNetworkSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	NMState *apiextensionsv1.JSON `json:"nmstate,omitempty"`
}

type HostStorageSpec struct {
	// +optional
	Devices []string `json:"devices,omitempty"`
}

type BMCSpec struct {
	Address   string `json:"address"`
	SecretRef string `json:"secretRef"`
}

type HostSpec struct {
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Enum=control-plane;ceph-osd;fabric-gateway;workload
	Roles []string `json:"roles"`
	// +optional
	Management ManagementSpec `json:"management,omitempty"`
	// +optional
	MaintenanceWindow string `json:"maintenanceWindow,omitempty"`
	// +optional
	Maintenance bool `json:"maintenance,omitempty"`
	// +optional
	Network HostNetworkSpec `json:"network,omitempty"`
	// +optional
	Storage HostStorageSpec `json:"storage,omitempty"`
	// +optional
	BMC *BMCSpec `json:"bmc,omitempty"`
}

type CPUInfo struct {
	Model   string `json:"model,omitempty"`
	Cores   int32  `json:"cores,omitempty"`
	Threads int32  `json:"threads,omitempty"`
	Sockets int32  `json:"sockets,omitempty"`
}

type DiskInfo struct {
	Path       string `json:"path"`
	Model      string `json:"model,omitempty"`
	Serial     string `json:"serial,omitempty"`
	SizeBytes  int64  `json:"sizeBytes"`
	Rotational bool   `json:"rotational"`
	WWN        string `json:"wwn,omitempty"`
	InUseBy    string `json:"inUseBy,omitempty"`
}

type NICInfo struct {
	Name       string `json:"name"`
	MAC        string `json:"mac"`
	SpeedMbps  int32  `json:"speedMbps,omitempty"`
	Link       bool   `json:"link"`
	PCIAddress string `json:"pciAddress,omitempty"`
	Driver     string `json:"driver,omitempty"`
}

type PCIInfo struct {
	Address    string `json:"address"`
	Vendor     string `json:"vendor"`
	Device     string `json:"device"`
	Class      string `json:"class"`
	IOMMUGroup int32  `json:"iommuGroup,omitempty"`
	Driver     string `json:"driver,omitempty"`
}

type FirmwareInfo struct {
	BIOS      string `json:"bios,omitempty"`
	Kernel    string `json:"kernel,omitempty"`
	OS        string `json:"os,omitempty"`
	OSVersion string `json:"osVersion,omitempty"`
}

type Inventory struct {
	// +optional
	CPU CPUInfo `json:"cpu,omitempty"`
	// +optional
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
	// +optional
	Disks []DiskInfo `json:"disks,omitempty"`
	// +optional
	NICs []NICInfo `json:"nics,omitempty"`
	// +optional
	PCI []PCIInfo `json:"pci,omitempty"`
	// +optional
	Firmware FirmwareInfo `json:"firmware,omitempty"`
}

type StepResult struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type AppliedConfig struct {
	// +optional
	Generation int64 `json:"generation,omitempty"`
	// +optional
	Steps []StepResult `json:"steps,omitempty"`
}

type DepotBundle struct {
	Version string `json:"version"`
	Arch    string `json:"arch"`
	Bytes   int64  `json:"bytes"`
}

type DepotStatus struct {
	URL string `json:"url"`
	// +optional
	Bundles []DepotBundle `json:"bundles,omitempty"`
}

type HostChecks struct {
	// +optional
	TimeSynced bool `json:"timeSynced,omitempty"`
	// +optional
	VarLibFreeBytes int64 `json:"varLibFreeBytes,omitempty"`
	// +optional
	CertificatesNotAfter *metav1.Time `json:"certificatesNotAfter,omitempty"`
	// +optional
	EtcdMembers int32 `json:"etcdMembers,omitempty"`
	// +optional
	ImagesBytes int64 `json:"imagesBytes,omitempty"`
}

type RestoreStatus struct {
	Backup      string      `json:"backup"`
	CompletedAt metav1.Time `json:"completedAt"`
}

type HostStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Inventory Inventory `json:"inventory,omitempty"`
	// +optional
	Applied *AppliedConfig `json:"applied,omitempty"`
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`
	// +optional
	AgentVersion string `json:"agentVersion,omitempty"`
	// +optional
	K0sVersion string `json:"k0sVersion,omitempty"`
	// +optional
	Hostname string `json:"hostname,omitempty"`
	// +optional
	Depot *DepotStatus `json:"depot,omitempty"`
	// +optional
	Checks *HostChecks `json:"checks,omitempty"`
	// +optional
	Restore *RestoreStatus `json:"restore,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Roles",type=string,JSONPath=`.spec.roles`
// +kubebuilder:printcolumn:name="Managed",type=boolean,JSONPath=`.spec.management.enabled`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type Host struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              HostSpec   `json:"spec"`
	Status            HostStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type HostList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Host `json:"items"`
}

func HostHasRole(host Host, role string) bool {
	return slices.Contains(host.Spec.Roles, role)
}

func init() {
	SchemeBuilder.Register(&Host{}, &HostList{})
}
