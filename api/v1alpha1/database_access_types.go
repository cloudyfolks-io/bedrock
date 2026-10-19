package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DatabaseGrantReadOnly  = "readonly"
	DatabaseGrantReadWrite = "readwrite"
	DatabaseGrantAll       = "all"

	DatabaseSchemaRetain = "Retain"
	DatabaseSchemaDelete = "Delete"
)

type DatabaseGrant struct {
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]{1,62}$`
	Database string `json:"database"`
	// +kubebuilder:validation:Enum=readonly;readwrite;all
	Level string `json:"level"`
}

// +kubebuilder:validation:XValidation:rule="has(self.username) == has(oldSelf.username) && (!has(self.username) || self.username == oldSelf.username)",message="username is immutable"
type DatabaseUserSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="databaseRef is immutable"
	DatabaseRef string `json:"databaseRef"`
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]{1,62}$`
	// +optional
	Username string `json:"username,omitempty"`
	// +kubebuilder:validation:MaxItems=50
	// +optional
	// +listType=map
	// +listMapKey=database
	Grants []DatabaseGrant `json:"grants,omitempty"`
}

type DatabaseUserStatus struct {
	CommonStatus `json:",inline"`
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Username string `json:"username,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	AppliedGrants string `json:"appliedGrants,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 52",message="the name must have at most 52 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.databaseRef`
// +kubebuilder:printcolumn:name="Username",type=string,JSONPath=`.status.username`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type DatabaseUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DatabaseUserSpec   `json:"spec"`
	Status            DatabaseUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DatabaseUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DatabaseUser `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="has(self.name) == has(oldSelf.name) && (!has(self.name) || self.name == oldSelf.name)",message="name is immutable"
type DatabaseSchemaSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="databaseRef is immutable"
	DatabaseRef string `json:"databaseRef"`
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]{1,62}$`
	// +optional
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	// +optional
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

type DatabaseSchemaStatus struct {
	CommonStatus `json:",inline"`
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Name string `json:"name,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 52",message="the name must have at most 52 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.databaseRef`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.status.name`
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=`.spec.deletionPolicy`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type DatabaseSchema struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DatabaseSchemaSpec   `json:"spec"`
	Status            DatabaseSchemaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DatabaseSchemaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DatabaseSchema `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DatabaseUser{}, &DatabaseUserList{}, &DatabaseSchema{}, &DatabaseSchemaList{})
}
