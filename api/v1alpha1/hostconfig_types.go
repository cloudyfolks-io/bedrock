package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type UnitSpec struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Enabled bool   `json:"enabled"`
}

type MirrorSpec struct {
	Registry string `json:"registry"`
	Endpoint string `json:"endpoint"`
}

type TenantLegSpec struct {
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]+$`
	Interface string `json:"interface"`
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=43
	// +kubebuilder:validation:items:XValidation:rule="isCIDR(self)",message="every allowSources entry must be a CIDR"
	// +kubebuilder:validation:items:XValidation:rule="!isCIDR(self) || cidr(self).prefixLength() > 0",message="allowSources must not match every address"
	AllowSources []string `json:"allowSources,omitempty"`
}

type HostConfigSpec struct {
	// +optional
	Packages []string `json:"packages,omitempty"`
	// +optional
	Modules []string `json:"modules,omitempty"`
	// +optional
	Sysctls map[string]string `json:"sysctls,omitempty"`
	// +optional
	KernelCmdline []string `json:"kernelCmdline,omitempty"`
	// +optional
	Units []UnitSpec `json:"units,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	NMState *apiextensionsv1.JSON `json:"nmstate,omitempty"`
	// +optional
	ContainerdMirrors []MirrorSpec `json:"containerdMirrors,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=interface
	// +kubebuilder:validation:MaxItems=16
	TenantLegs []TenantLegSpec `json:"tenantLegs,omitempty"`
}

type HostConfigStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type HostConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              HostConfigSpec   `json:"spec"`
	Status            HostConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type HostConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HostConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HostConfig{}, &HostConfigList{})
}
