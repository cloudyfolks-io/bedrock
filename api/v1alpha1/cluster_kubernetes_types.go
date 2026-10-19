package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	KubernetesClusterPhasePending      = "Pending"
	KubernetesClusterPhaseProvisioning = "Provisioning"
	KubernetesClusterPhaseReady        = "Ready"
	KubernetesClusterPhaseSuspended    = "Suspended"
	KubernetesClusterPhaseFailed       = "Failed"
	KubernetesClusterPhaseDeleting     = "Deleting"

	KubernetesClusterFieldManager = "bedrock-kubernetescluster"
	KubernetesClusterFinalizer    = "bedrock.cloudyfolks.io/kubernetescluster"
)

type KubernetesClusterNetwork struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:default=default
	// +optional
	SubnetRef string `json:"subnetRef,omitempty"`
	// +kubebuilder:validation:MaxLength=18
	// +kubebuilder:validation:XValidation:rule="isCIDR(self) && cidr(self).ip().family() == 4",message="podCIDR must be an IPv4 CIDR"
	// +kubebuilder:default="100.64.0.0/16"
	// +optional
	PodCIDR string `json:"podCIDR,omitempty"`
	// +kubebuilder:validation:MaxLength=18
	// +kubebuilder:validation:XValidation:rule="isCIDR(self) && cidr(self).ip().family() == 4",message="serviceCIDR must be an IPv4 CIDR"
	// +kubebuilder:default="100.65.0.0/16"
	// +optional
	ServiceCIDR string `json:"serviceCIDR,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.address) == has(oldSelf.address) && (!has(self.address) || self.address == oldSelf.address)",message="address is immutable"
type KubernetesControlPlane struct {
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:validation:XValidation:rule="isIP(self) && ip(self).family() == 4",message="address must be an IPv4 address"
	// +optional
	Address string `json:"address,omitempty"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +listType=set
	SecurityGroups []string `json:"securityGroups"`
}

type KubernetesWorkerCompute struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	// +kubebuilder:default=2
	// +optional
	Cores int32 `json:"cores,omitempty"`
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=65536
	// +kubebuilder:default=4096
	// +optional
	MemoryMiB int32 `json:"memoryMiB,omitempty"`
}

type KubernetesWorkerDisk struct {
	// +kubebuilder:validation:Minimum=20
	// +kubebuilder:validation:Maximum=500
	// +kubebuilder:default=40
	// +optional
	SizeGi int32 `json:"sizeGi,omitempty"`
}

type KubernetesWorker struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:default={}
	// +optional
	Compute KubernetesWorkerCompute `json:"compute"`
	// +kubebuilder:default={}
	// +optional
	RootDisk KubernetesWorkerDisk `json:"rootDisk"`
	Image    ImageRef             `json:"image"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +listType=set
	SecurityGroups []string `json:"securityGroups"`
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:validation:XValidation:rule="isIP(self) && ip(self).family() == 4",message="address must be an IPv4 address"
	// +optional
	Address string `json:"address,omitempty"`
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	// +listType=set
	SSHKeys []string `json:"sshKeys,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.workers) || !has(oldSelf.workers) || self.workers.all(n, !oldSelf.workers.exists(o, o.name == n.name && (has(o.address) != has(n.address) || (has(n.address) && o.address != n.address))))",message="a worker address is immutable"
type KubernetesClusterSpec struct {
	// +kubebuilder:validation:MaxLength=50
	// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+\.[0-9]+\+k0s\.[0-9]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="version is immutable"
	Version string `json:"version"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="network is immutable"
	// +kubebuilder:default={}
	// +optional
	Network      KubernetesClusterNetwork `json:"network"`
	ControlPlane KubernetesControlPlane   `json:"controlPlane"`
	// +kubebuilder:validation:MaxItems=50
	// +optional
	// +listType=map
	// +listMapKey=name
	Workers []KubernetesWorker `json:"workers,omitempty"`
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

type KubernetesControlPlaneStatus struct {
	// +optional
	Address string `json:"address,omitempty"`
	// +optional
	Ready bool `json:"ready,omitempty"`
}

type KubernetesWorkerStatus struct {
	Name string `json:"name"`
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	VM string `json:"vm,omitempty"`
}

type KubernetesClusterStatus struct {
	CommonStatus `json:",inline"`
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Suspended;Failed;Deleting
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	ControlPlane KubernetesControlPlaneStatus `json:"controlPlane"`
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// +optional
	KubeconfigSecret string `json:"kubeconfigSecret,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=name
	Workers []KubernetesWorkerStatus `json:"workers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 40",message="the name must have at most 40 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.workers) || self.spec.workers.all(w, self.metadata.name.size() + w.name.size() + 1 <= 52)",message="the cluster name, a dash and a worker name must have at most 52 characters"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
type KubernetesCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KubernetesClusterSpec   `json:"spec"`
	Status            KubernetesClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KubernetesClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubernetesCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KubernetesCluster{}, &KubernetesClusterList{})
}
