package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ImageTypeDisk = "disk"
	ImageTypeISO  = "iso"

	ImageScopeCluster = "cluster"
	ImageScopeProject = "project"

	ImagePhasePending          = "Pending"
	ImagePhaseWaitingForUpload = "WaitingForUpload"
	ImagePhaseImporting        = "Importing"
	ImagePhaseReady            = "Ready"
	ImagePhaseFailed           = "Failed"
)

type ImageRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=cluster;project
	// +kubebuilder:default=cluster
	// +optional
	Scope string `json:"scope,omitempty"`
}

type ImageOS struct {
	// +optional
	Family string `json:"family,omitempty"`
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	Preference string `json:"preference,omitempty"`
}

type ImageRegistrySource struct {
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
	// +optional
	PullSecretRef string `json:"pullSecretRef,omitempty"`
}

type ImageHTTPSource struct {
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// +optional
	Checksum string `json:"checksum,omitempty"`
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
	// +optional
	CAConfigMap string `json:"caConfigMap,omitempty"`
}

type ImageUploadSource struct{}

type ImageVolumeSource struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// +kubebuilder:validation:XValidation:rule="[has(self.registry), has(self.http), has(self.upload), has(self.volumeRef)].filter(set, set).size() == 1",message="exactly one source must be set"
type ImageSource struct {
	// +optional
	Registry *ImageRegistrySource `json:"registry,omitempty"`
	// +optional
	HTTP *ImageHTTPSource `json:"http,omitempty"`
	// +optional
	Upload *ImageUploadSource `json:"upload,omitempty"`
	// +optional
	VolumeRef *ImageVolumeSource `json:"volumeRef,omitempty"`
}

type ImageRefresh struct {
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	Keep int32 `json:"keep,omitempty"`
}

type ImageSpec struct {
	// +kubebuilder:validation:Enum=disk;iso
	// +kubebuilder:default=disk
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="type is immutable"
	// +optional
	Type string `json:"type,omitempty"`
	// +optional
	OS     *ImageOS    `json:"os,omitempty"`
	Source ImageSource `json:"source"`
	// +optional
	Refresh *ImageRefresh `json:"refresh,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	SizeGi int32 `json:"sizeGi,omitempty"`
}

type ImageVersion struct {
	Digest string `json:"digest"`
	// +optional
	Time *metav1.Time `json:"time,omitempty"`
	// +optional
	DataSource string `json:"dataSource,omitempty"`
}

type ImageStatus struct {
	CommonStatus `json:",inline"`
	// +kubebuilder:validation:Enum=Pending;WaitingForUpload;Importing;Ready;Failed
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Digest string `json:"digest,omitempty"`
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	// +optional
	DataSource string `json:"dataSource,omitempty"`
	// +optional
	UploadURL string `json:"uploadURL,omitempty"`
	// +optional
	// +listType=atomic
	Versions []ImageVersion `json:"versions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type ClusterImage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ImageSpec   `json:"spec"`
	Status            ImageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterImage `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type Image struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ImageSpec   `json:"spec"`
	Status            ImageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Image `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterImage{}, &ClusterImageList{}, &Image{}, &ImageList{})
}
