package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ContainerPhasePending = "Pending"
	ContainerPhaseRunning = "Running"
	ContainerPhaseStopped = "Stopped"
	ContainerPhaseFailed  = "Failed"

	ContainerStrategyRecreate      = "Recreate"
	ContainerStrategyRollingUpdate = "RollingUpdate"

	ContainerProtocolTCP  = "TCP"
	ContainerProtocolUDP  = "UDP"
	ContainerProtocolSCTP = "SCTP"

	ContainerFieldManager = "bedrock-container"

	AnnotationRestartedAt = "bedrock.cloudyfolks.io/restarted-at"
)

// +kubebuilder:validation:XValidation:rule="has(self.value) != has(self.valueFrom)",message="set exactly one of value and valueFrom"
type ContainerEnvVar struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z][-._a-zA-Z0-9]*$`
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=32768
	// +optional
	Value string `json:"value,omitempty"`
	// +optional
	ValueFrom *ContainerEnvSource `json:"valueFrom,omitempty"`
}

type ContainerEnvSource struct {
	SecretKeyRef *ContainerSecretKeyRef `json:"secretKeyRef"`
}

type ContainerSecretKeyRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	Key string `json:"key"`
}

type ContainerPort struct {
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
	// +kubebuilder:validation:Enum=TCP;UDP;SCTP
	// +kubebuilder:default=TCP
	// +optional
	Protocol string `json:"protocol,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.requests) || !has(self.limits) || ((!has(self.requests.cpu) || !has(self.limits.cpu) || quantity(string(self.requests.cpu)).compareTo(quantity(string(self.limits.cpu))) <= 0) && (!has(self.requests.memory) || !has(self.limits.memory) || quantity(string(self.requests.memory)).compareTo(quantity(string(self.limits.memory))) <= 0))",message="requests must not be above limits"
// +kubebuilder:validation:XValidation:rule="!has(self.limits) || !has(self.limits.cpu) || quantity(string(self.limits.cpu)).compareTo(quantity('32')) <= 0",message="limits.cpu must be at most 32"
// +kubebuilder:validation:XValidation:rule="!has(self.limits) || !has(self.limits.memory) || quantity(string(self.limits.memory)).compareTo(quantity('256Gi')) <= 0",message="limits.memory must be at most 256Gi"
type ContainerResources struct {
	// +kubebuilder:default={}
	// +optional
	Requests ContainerRequests `json:"requests"`
	// +kubebuilder:default={}
	// +optional
	Limits ContainerLimits `json:"limits"`
}

type ContainerRequests struct {
	// +kubebuilder:default="100m"
	// +optional
	CPU *resource.Quantity `json:"cpu,omitempty"`
	// +kubebuilder:default="128Mi"
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`
}

type ContainerLimits struct {
	// +kubebuilder:default="500m"
	// +optional
	CPU *resource.Quantity `json:"cpu,omitempty"`
	// +kubebuilder:default="512Mi"
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`
}

type ContainerExec struct {
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	Command []string `json:"command"`
}

// +kubebuilder:validation:XValidation:rule="has(self.exec)",message="only exec probes are supported"
type ContainerProbe struct {
	// +optional
	Exec *ContainerExec `json:"exec,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	InitialDelaySeconds *int32 `json:"initialDelaySeconds,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	PeriodSeconds *int32 `json:"periodSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	SuccessThreshold *int32 `json:"successThreshold,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	FailureThreshold *int32 `json:"failureThreshold,omitempty"`
}

type ContainerProbes struct {
	// +optional
	Liveness *ContainerProbe `json:"liveness,omitempty"`
	// +optional
	Readiness *ContainerProbe `json:"readiness,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.min <= self.max",message="min must not be above max"
type ContainerAutoscale struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	Min int32 `json:"min"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	Max int32 `json:"max"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	CPUPercent *int32 `json:"cpuPercent,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	MemoryPercent *int32 `json:"memoryPercent,omitempty"`
}

type ContainerVolume struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	VolumeRef string `json:"volumeRef"`
	// +kubebuilder:validation:MaxLength=4096
	// +kubebuilder:validation:Pattern=`^/[^:]+$`
	MountPath string `json:"mountPath"`
}

// +kubebuilder:validation:XValidation:rule="has(self.address) == has(oldSelf.address) && (!has(self.address) || self.address == oldSelf.address)",message="address is immutable"
type ContainerNetwork struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="subnetRef is immutable"
	// +kubebuilder:default=default
	// +optional
	SubnetRef string `json:"subnetRef,omitempty"`
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:validation:XValidation:rule="isIP(self) && ip(self).family() == 4",message="address must be an IPv4 address"
	// +optional
	Address string `json:"address,omitempty"`
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	// +listType=set
	SecurityGroups []string `json:"securityGroups,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.replicas) || !has(self.autoscale)",message="replicas and autoscale exclude each other"
// +kubebuilder:validation:XValidation:rule="!has(self.network.address) || (!has(self.autoscale) && (!has(self.replicas) || self.replicas <= 1))",message="address needs replicas of at most 1 and no autoscale"
// +kubebuilder:validation:XValidation:rule="!has(self.volumes) || self.volumes.size() == 0 || (!has(self.autoscale) && (!has(self.replicas) || self.replicas <= 1))",message="volumes need replicas of at most 1 and no autoscale"
// +kubebuilder:validation:XValidation:rule="!has(self.strategy) || self.strategy == 'Recreate' || (!has(self.network.address) && (!has(self.volumes) || self.volumes.size() == 0))",message="address and volumes need the strategy Recreate"
type ContainerSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Image string `json:"image"`
	// +kubebuilder:validation:MaxItems=64
	// +optional
	// +listType=atomic
	Command []string `json:"command,omitempty"`
	// +kubebuilder:validation:MaxItems=256
	// +optional
	// +listType=atomic
	Args []string `json:"args,omitempty"`
	// +kubebuilder:validation:MaxItems=128
	// +optional
	// +listType=map
	// +listMapKey=name
	Env []ContainerEnvVar `json:"env,omitempty"`
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:XValidation:rule="self.all(a, self.exists_one(b, b.port == a.port && (has(a.protocol) ? a.protocol : 'TCP') == (has(b.protocol) ? b.protocol : 'TCP')))",message="a port and protocol can be listed once"
	// +optional
	// +listType=atomic
	Ports []ContainerPort `json:"ports,omitempty"`
	// +kubebuilder:default={}
	// +optional
	Resources ContainerResources `json:"resources"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=20
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
	// +kubebuilder:validation:Enum=Recreate;RollingUpdate
	// +optional
	Strategy string `json:"strategy,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	FSGroup *int64 `json:"fsGroup,omitempty"`
	// +optional
	Probes *ContainerProbes `json:"probes,omitempty"`
	// +optional
	Autoscale *ContainerAutoscale `json:"autoscale,omitempty"`
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:XValidation:rule="self.all(a, self.exists_one(b, b.volumeRef == a.volumeRef))",message="a volume can be listed once"
	// +kubebuilder:validation:XValidation:rule="self.all(a, self.exists_one(b, b.mountPath == a.mountPath))",message="a mount path can be used once"
	// +optional
	// +listType=atomic
	Volumes []ContainerVolume `json:"volumes,omitempty"`
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	// +listType=set
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`
	// +kubebuilder:default={}
	// +optional
	Network ContainerNetwork `json:"network"`
}

type ContainerStatus struct {
	CommonStatus `json:",inline"`
	// +kubebuilder:validation:Enum=Pending;Running;Stopped;Failed
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// +optional
	// +listType=atomic
	Addresses []string `json:"addresses,omitempty"`
	// +optional
	Deployment string `json:"deployment,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 52",message="the name must have at most 52 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.status.replicas`
type Container struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ContainerSpec   `json:"spec"`
	Status            ContainerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ContainerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Container `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Container{}, &ContainerList{})
}
