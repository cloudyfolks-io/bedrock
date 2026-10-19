package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ObjectStorageUserSpec struct {
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +kubebuilder:validation:Enum=read;readWrite
	Permission string `json:"permission"`
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +listType=set
	// +optional
	Buckets []string `json:"buckets,omitempty"`
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

type ObjectStorageUserStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	// +optional
	ARN string `json:"arn,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Permission",type=string,JSONPath=`.spec.permission`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
type ObjectStorageUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ObjectStorageUserSpec   `json:"spec"`
	Status            ObjectStorageUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ObjectStorageUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObjectStorageUser `json:"items"`
}

type ObjectStorageBucketQuota struct {
	// +optional
	Bytes *resource.Quantity `json:"bytes,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Objects *int64 `json:"objects,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.bucketName) == has(oldSelf.bucketName) && (!has(self.bucketName) || self.bucketName == oldSelf.bucketName)",message="bucketName is immutable"
type ObjectStorageBucketSpec struct {
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`
	// +optional
	BucketName string `json:"bucketName,omitempty"`
	// +optional
	Quota ObjectStorageBucketQuota `json:"quota,omitempty"`
	// +optional
	Versioning bool `json:"versioning,omitempty"`
	// +kubebuilder:validation:MaxLength=20480
	// +optional
	Policy string `json:"policy,omitempty"`
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	CORSOrigins []string `json:"corsOrigins,omitempty"`
	// +optional
	ForceDestroy bool `json:"forceDestroy,omitempty"`
}

type ObjectStorageBucketUsage struct {
	Bytes     int64       `json:"bytes"`
	Objects   int64       `json:"objects"`
	SampledAt metav1.Time `json:"sampledAt"`
}

type ObjectStorageBucketStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// +optional
	BucketName string `json:"bucketName,omitempty"`
	// +optional
	Usage *ObjectStorageBucketUsage `json:"usage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.status.bucketName`
// +kubebuilder:printcolumn:name="Bytes",type=integer,JSONPath=`.status.usage.bytes`
// +kubebuilder:printcolumn:name="Objects",type=integer,JSONPath=`.status.usage.objects`
type ObjectStorageBucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ObjectStorageBucketSpec   `json:"spec"`
	Status            ObjectStorageBucketStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ObjectStorageBucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObjectStorageBucket `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ObjectStorageUser{}, &ObjectStorageUserList{}, &ObjectStorageBucket{}, &ObjectStorageBucketList{})
}
