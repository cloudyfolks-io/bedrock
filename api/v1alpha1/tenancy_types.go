package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	MemberRoleOwner  = "owner"
	MemberRoleEditor = "editor"
	MemberRoleViewer = "viewer"
)

type TenantQuota struct {
	// +optional
	CPU *resource.Quantity `json:"cpu,omitempty"`
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`
	// +optional
	Storage *resource.Quantity `json:"storage,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	ElasticIPs *int32 `json:"elasticIPs,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	Projects *int32 `json:"projects,omitempty"`
}

type TenantNetworkSpec struct {
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="vpcCIDR must be a CIDR"
	// +kubebuilder:validation:XValidation:rule="!isCIDR(self) || (cidr(self).ip().family() == 4 && cidr(self).prefixLength() <= 24)",message="vpcCIDR must be IPv4 with a prefix length of at most 24"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="vpcCIDR is immutable"
	VPCCIDR string `json:"vpcCIDR"`
}

type TenantSpec struct {
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	// +listType=atomic
	Owners []SubjectRef `json:"owners,omitempty"`
	// +optional
	Quota   TenantQuota       `json:"quota,omitempty"`
	Network TenantNetworkSpec `json:"network"`
}

type TenantStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	VPC string `json:"vpc,omitempty"`
	// +optional
	Usage TenantQuota `json:"usage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="VPC",type=string,JSONPath=`.status.vpc`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 30",message="the name must have at most 30 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]+$')",message="the name must be lowercase letters and digits only"
type Tenant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TenantSpec   `json:"spec"`
	Status            TenantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tenant `json:"items"`
}

type ProjectQuota struct {
	// +optional
	CPU *resource.Quantity `json:"cpu,omitempty"`
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`
	// +optional
	Storage *resource.Quantity `json:"storage,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	VMs *int32 `json:"vms,omitempty"`
}

type ProjectMember struct {
	Ref SubjectRef `json:"ref"`
	// +kubebuilder:validation:Enum=owner;editor;viewer
	Role string `json:"role"`
}

type ProjectSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=30
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tenantRef is immutable"
	TenantRef string `json:"tenantRef"`
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	// +listType=atomic
	Members []ProjectMember `json:"members,omitempty"`
	// +optional
	Quota ProjectQuota `json:"quota,omitempty"`
}

type ProjectStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	Subnet string `json:"subnet,omitempty"`
	// +optional
	Usage ProjectQuota `json:"usage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tenant",type=string,JSONPath=`.spec.tenantRef`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 30",message="the name must have at most 30 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
type Project struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProjectSpec   `json:"spec"`
	Status            ProjectStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Project `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="has(self.cidr) != has(self.cidrs)",message="set exactly one of cidr and cidrs; cidrs replaces cidr"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.cidr) || (has(self.cidr) && self.cidr == oldSelf.cidr) || (has(self.cidrs) && oldSelf.cidr in self.cidrs)",message="cidr can only be replaced by a cidrs list that holds it"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.cidrs) || (has(self.cidrs) && oldSelf.cidrs.all(c, c in self.cidrs))",message="cidrs can only be added to"
type NetworkSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=30
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tenantRef is immutable"
	TenantRef string `json:"tenantRef"`
	// +optional
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="cidr must be a CIDR"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="cidr is immutable"
	CIDR string `json:"cidr,omitempty"`
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=43
	// +kubebuilder:validation:items:XValidation:rule="isCIDR(self) && cidr(self).ip().family() == 4 && string(cidr(self).masked()) == self",message="every cidrs entry must be a canonical IPv4 network"
	// +kubebuilder:validation:XValidation:rule="self.all(a, self.all(b, a == b || !isCIDR(a) || !isCIDR(b) || !(cidr(a).containsCIDR(b) || cidr(b).containsCIDR(a))))",message="the cidrs must not overlap each other"
	CIDRs []string `json:"cidrs,omitempty"`
	// +optional
	EnableExternal bool `json:"enableExternal,omitempty"`
}

func (s NetworkSpec) Ranges() []string {
	if len(s.CIDRs) > 0 {
		return s.CIDRs
	}
	if s.CIDR != "" {
		return []string{s.CIDR}
	}
	return nil
}

type NetworkSubnetStatus struct {
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=43
	CIDR string `json:"cidr"`
}

type NetworkStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	VPC string `json:"vpc,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=namespace
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=512
	Subnets []NetworkSubnetStatus `json:"subnets,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tenant",type=string,JSONPath=`.spec.tenantRef`
// +kubebuilder:printcolumn:name="CIDR",type=string,JSONPath=`.spec.cidr`
// +kubebuilder:printcolumn:name="CIDRs",type=string,JSONPath=`.spec.cidrs`
// +kubebuilder:printcolumn:name="VPC",type=string,JSONPath=`.status.vpc`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 30 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label of at most 30 characters"
// +kubebuilder:validation:XValidation:rule="!self.metadata.name.startsWith('provider-') && self.metadata.name != 'ovn-cluster'",message="the name is reserved for the Fabric underlay"
type Network struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NetworkSpec   `json:"spec"`
	Status            NetworkStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NetworkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Network `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Tenant{}, &TenantList{}, &Project{}, &ProjectList{}, &Network{}, &NetworkList{})
}
