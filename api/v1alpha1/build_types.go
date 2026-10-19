package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	BuildTypeDockerfile = "dockerfile"
	BuildTypeBuildpacks = "buildpacks"

	BuildSizeSmall  = "s"
	BuildSizeMedium = "m"
	BuildSizeLarge  = "l"

	BuildPhasePending   = "Pending"
	BuildPhaseRunning   = "Running"
	BuildPhaseSucceeded = "Succeeded"
	BuildPhaseFailed    = "Failed"
)

type BuildGitSource struct {
	// +kubebuilder:validation:MinLength=9
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^https://[^\s/@]+(/[^\s]*)?$`
	URL string `json:"url"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^[^\s]+$`
	Revision string `json:"revision"`
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

type BuildSource struct {
	Git BuildGitSource `json:"git"`
}

type BuildOutput struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9.-]*(:[0-9]+)?(/[a-z0-9._-]+)+(:[A-Za-z0-9._-]+)?$`
	Image string `json:"image"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	PushSecretRef string `json:"pushSecretRef"`
}

type BuildCache struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

type BuildNetwork struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	SubnetRef string `json:"subnetRef"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="a build is immutable"
type BuildSpec struct {
	// +kubebuilder:validation:Enum=dockerfile;buildpacks
	// +kubebuilder:default=dockerfile
	// +optional
	Type   string      `json:"type,omitempty"`
	Source BuildSource `json:"source"`
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('/') && !self.split('/').exists(part, part == '..')",message="the path must be relative and without .."
	// +kubebuilder:default="."
	// +optional
	ContextPath string `json:"contextPath,omitempty"`
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('/') && !self.split('/').exists(part, part == '..')",message="the path must be relative and without .."
	// +kubebuilder:default=Dockerfile
	// +optional
	DockerfilePath string      `json:"dockerfilePath,omitempty"`
	Output         BuildOutput `json:"output"`
	// +kubebuilder:default={}
	// +optional
	Cache BuildCache `json:"cache"`
	// +kubebuilder:validation:Enum=s;m;l
	// +kubebuilder:default=m
	// +optional
	Size string `json:"size,omitempty"`
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
	Network BuildNetwork     `json:"network"`
}

type BuildStatus struct {
	CommonStatus `json:",inline"`
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	JobName string `json:"jobName,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// +optional
	Image string `json:"image,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 52",message="the name must have at most 52 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.output.image`
type Build struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BuildSpec   `json:"spec"`
	Status            BuildStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Build `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Build{}, &BuildList{})
}
