package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:XValidation:rule="has(self.connectCIDR) == has(oldSelf.connectCIDR) && (!has(self.connectCIDR) || self.connectCIDR == oldSelf.connectCIDR)",message="connectCIDR is immutable"
// +kubebuilder:validation:XValidation:rule="self.networkRefs == oldSelf.networkRefs",message="networkRefs is immutable"
type NetworkPeeringSpec struct {
	// +kubebuilder:validation:MinItems=2
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=30
	// +kubebuilder:validation:XValidation:rule="self[0] != self[1]",message="a Network cannot peer with itself"
	// +listType=atomic
	NetworkRefs []string `json:"networkRefs"`
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self) && cidr(self).ip().family() == 4 && string(cidr(self).masked()) == self && cidr(self).prefixLength() == 30",message="connectCIDR must be a canonical IPv4 network with a prefix of 30"
	// +optional
	ConnectCIDR string `json:"connectCIDR,omitempty"`
}

type NetworkPeeringStatus struct {
	CommonStatus `json:",inline"`
	// +kubebuilder:validation:MaxLength=43
	// +optional
	ConnectCIDR string `json:"connectCIDR,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Networks",type=string,JSONPath=`.spec.networkRefs`
// +kubebuilder:printcolumn:name="Connect",type=string,JSONPath=`.status.connectCIDR`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
type NetworkPeering struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NetworkPeeringSpec   `json:"spec"`
	Status            NetworkPeeringStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NetworkPeeringList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkPeering `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetworkPeering{}, &NetworkPeeringList{})
}
