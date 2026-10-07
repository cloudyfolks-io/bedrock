package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ObjectStorageUserQuota struct {
	// +optional
	Bytes *resource.Quantity `json:"bytes,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Buckets *int32 `json:"buckets,omitempty"`
}

type ObjectStorageUserSpec struct {
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	Quota ObjectStorageUserQuota `json:"quota,omitempty"`
}

type ObjectStorageUserStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
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

type ObjectStorageBucketSpec struct {
	// +kubebuilder:validation:MinLength=1
	UserRef string `json:"userRef"`
	// +optional
	Quota ObjectStorageBucketQuota `json:"quota,omitempty"`
	// +optional
	Versioning bool `json:"versioning,omitempty"`
	// +kubebuilder:validation:MaxLength=20480
	// +optional
	Policy string `json:"policy,omitempty"`
}

type ObjectStorageBucketStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// +optional
	BucketName string `json:"bucketName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.userRef`
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.status.bucketName`
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
