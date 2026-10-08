package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ElasticIPTargetVirtualMachine = "VirtualMachine"
	ElasticIPTargetLoadBalancer   = "LoadBalancer"

	LoadBalancerProtocolTCP = "TCP"
	LoadBalancerProtocolUDP = "UDP"

	SecurityGroupProtocolAll  = "all"
	SecurityGroupProtocolICMP = "icmp"
	SecurityGroupProtocolTCP  = "tcp"
	SecurityGroupProtocolUDP  = "udp"
)

// +kubebuilder:validation:XValidation:rule="isCIDR(self.cidr) && cidr(self.cidr).ip().family() == 4 && string(cidr(self.cidr).masked()) == self.cidr && cidr(self.cidr).prefixLength() >= 16 && cidr(self.cidr).prefixLength() <= 28",message="cidr must be an IPv4 network address with a prefix from 16 to 28"
// +kubebuilder:validation:XValidation:rule="!has(self.gateway) || size(self.gateway) == 0 || (isIP(self.gateway) && cidr(self.cidr).containsIP(self.gateway))",message="gateway must be an address inside cidr"
type SubnetSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="networkRef is immutable"
	NetworkRef string `json:"networkRef"`
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="cidr must be a CIDR"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="cidr is immutable"
	CIDR string `json:"cidr"`
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="isIP(self)",message="gateway must be an IP address"
	// +optional
	Gateway string `json:"gateway,omitempty"`
	// +kubebuilder:default=true
	// +optional
	DHCP bool `json:"dhcp"`
	// +optional
	// +listType=set
	ExcludeIPs []string `json:"excludeIPs,omitempty"`
	// +optional
	DNSZoneRef string `json:"dnsZoneRef,omitempty"`
}

type SubnetStatus struct {
	CommonStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Network",type=string,JSONPath=`.spec.networkRef`
// +kubebuilder:printcolumn:name="CIDR",type=string,JSONPath=`.spec.cidr`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
type Subnet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SubnetSpec   `json:"spec"`
	Status            SubnetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SubnetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Subnet `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.subnetRef) || self.kind == 'VirtualMachine'",message="subnetRef selects a VirtualMachine interface"
type ElasticIPTarget struct {
	// +kubebuilder:validation:Enum=VirtualMachine;LoadBalancer
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	SubnetRef string `json:"subnetRef,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.providerNetwork == oldSelf.providerNetwork",message="providerNetwork is immutable"
// +kubebuilder:validation:XValidation:rule="has(self.address) == has(oldSelf.address) && (!has(self.address) || self.address == oldSelf.address)",message="address is immutable"
type ElasticIPSpec struct {
	// +kubebuilder:validation:MinLength=1
	ProviderNetwork string `json:"providerNetwork"`
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="isIP(self)",message="address must be an IP address"
	// +optional
	Address string `json:"address,omitempty"`
	// +optional
	Target *ElasticIPTarget `json:"target,omitempty"`
}

type ElasticIPStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	Address string `json:"address,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.address`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.name`
type ElasticIP struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ElasticIPSpec   `json:"spec"`
	Status            ElasticIPStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ElasticIPList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ElasticIP `json:"items"`
}

type LoadBalancerTarget struct {
	// +kubebuilder:validation:MinLength=1
	VMRef string `json:"vmRef"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

type LoadBalancerHealthCheck struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=5
	// +optional
	IntervalSeconds int32 `json:"intervalSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	// +optional
	SuccessThreshold int32 `json:"successThreshold,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	// +optional
	FailureThreshold int32 `json:"failureThreshold,omitempty"`
}

type LoadBalancerListener struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
	// +kubebuilder:validation:Enum=TCP;UDP
	// +kubebuilder:default=TCP
	Protocol string `json:"protocol"`
	// +optional
	// +listType=atomic
	Targets []LoadBalancerTarget `json:"targets,omitempty"`
	// +optional
	HealthCheck *LoadBalancerHealthCheck `json:"healthCheck,omitempty"`
}

type LoadBalancerSpec struct {
	// +optional
	ElasticIPRef string `json:"elasticIPRef,omitempty"`
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=port
	// +listMapKey=protocol
	Listeners []LoadBalancerListener `json:"listeners"`
}

type LoadBalancerStatus struct {
	CommonStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Elastic IP",type=string,JSONPath=`.spec.elasticIPRef`
type LoadBalancer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              LoadBalancerSpec   `json:"spec"`
	Status            LoadBalancerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type LoadBalancerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LoadBalancer `json:"items"`
}

type DNSRecord struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	IPs []string `json:"ips"`
}

type DNSZoneSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Zone string `json:"zone"`
	// +optional
	// +listType=map
	// +listMapKey=name
	Records []DNSRecord `json:"records,omitempty"`
}

type DNSZoneStatus struct {
	CommonStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zone`
type DNSZone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DNSZoneSpec   `json:"spec"`
	Status            DNSZoneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DNSZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DNSZone `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="has(self.cidr) != has(self.securityGroupRef)",message="exactly one of cidr and securityGroupRef must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.port) || self.protocol in ['tcp', 'udp']",message="port requires the tcp or udp protocol"
// +kubebuilder:validation:XValidation:rule="!has(self.endPort) || (has(self.port) && self.endPort >= self.port)",message="endPort requires port and must not be lower than port"
type SecurityGroupRule struct {
	// +kubebuilder:validation:Enum=all;icmp;tcp;udp
	// +kubebuilder:default=all
	// +optional
	Protocol string `json:"protocol,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	EndPort int32 `json:"endPort,omitempty"`
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="cidr must be a CIDR"
	// +optional
	CIDR string `json:"cidr,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +optional
	SecurityGroupRef string `json:"securityGroupRef,omitempty"`
}

type SecurityGroupSpec struct {
	// +kubebuilder:validation:MaxItems=100
	// +optional
	// +listType=atomic
	Ingress []SecurityGroupRule `json:"ingress,omitempty"`
	// +kubebuilder:validation:MaxItems=100
	// +optional
	// +listType=atomic
	Egress []SecurityGroupRule `json:"egress,omitempty"`
}

type SecurityGroupStatus struct {
	CommonStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
type SecurityGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SecurityGroupSpec   `json:"spec"`
	Status            SecurityGroupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SecurityGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SecurityGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Subnet{}, &SubnetList{}, &ElasticIP{}, &ElasticIPList{}, &LoadBalancer{}, &LoadBalancerList{}, &DNSZone{}, &DNSZoneList{}, &SecurityGroup{}, &SecurityGroupList{})
}
