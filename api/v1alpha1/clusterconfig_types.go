package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type PlatformSpec struct {
	Host string `json:"host"`
	// +kubebuilder:validation:Enum=SelfSigned;LetsEncrypt;Custom
	// +optional
	TLSMode string `json:"tlsMode,omitempty"`
}

type ProviderNetworkSpec struct {
	Name      string `json:"name"`
	Interface string `json:"interface"`
	// +optional
	Vlan    int32  `json:"vlan,omitempty"`
	CIDR    string `json:"cidr"`
	Gateway string `json:"gateway"`
}

type FabricSpec struct {
	// +optional
	PodCIDR string `json:"podCIDR,omitempty"`
	// +optional
	ServiceCIDR string `json:"serviceCIDR,omitempty"`
	// +optional
	JoinCIDR string `json:"joinCIDR,omitempty"`
	// +kubebuilder:validation:Enum=bgp;l2
	// +optional
	EIPMode string `json:"eipMode,omitempty"`
	// +optional
	ProviderNetworks []ProviderNetworkSpec `json:"providerNetworks,omitempty"`
}

type NetworkSpec struct {
	ManagementInterface string `json:"managementInterface"`
	// +optional
	StorageVlan int32 `json:"storageVlan,omitempty"`
	// +optional
	MigrationVlan int32 `json:"migrationVlan,omitempty"`
	// +optional
	Fabric FabricSpec `json:"fabric,omitempty"`
}

type StorageSpec struct {
	// +optional
	Devices []string `json:"devices,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
}

type VirtualizationSpec struct {
	// +optional
	Emulation bool `json:"emulation,omitempty"`
}

type BundleRegistrySpec struct {
	// +optional
	Bundle string `json:"bundle,omitempty"`
	// +optional
	Mirror string `json:"mirror,omitempty"`
}

type HostDefaults struct {
	// +optional
	Management bool `json:"management,omitempty"`
	// +optional
	MaintenanceWindow string `json:"maintenanceWindow,omitempty"`
}

type ClusterConfigSpec struct {
	Version string  `json:"version"`
	API     APISpec `json:"api"`
	// +optional
	Platform PlatformSpec `json:"platform,omitempty"`
	Network  NetworkSpec  `json:"network"`
	Storage  StorageSpec  `json:"storage"`
	// +optional
	Virtualization VirtualizationSpec `json:"virtualization,omitempty"`
	// +optional
	Registry BundleRegistrySpec `json:"registry,omitempty"`
	// +optional
	Addons map[string]bool `json:"addons,omitempty"`
	// +optional
	Host HostDefaults `json:"host,omitempty"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Enum=control-plane;ceph-osd;fabric-gateway;workload
	Roles []string `json:"roles"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
type ClusterConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterConfigSpec `json:"spec"`
}

func init() {
	SchemeBuilder.Register(&ClusterConfig{})
}
