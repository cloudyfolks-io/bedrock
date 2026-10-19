package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RegistryRetention struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	// +optional
	KeepLastN *int32 `json:"keepLastN,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=3650
	// +optional
	RetainDays *int32 `json:"retainDays,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	// +optional
	TagPattern string `json:"tagPattern,omitempty"`
	// +optional
	RetainUntagged bool `json:"retainUntagged,omitempty"`
	// +kubebuilder:validation:Enum=off;hourly;daily
	// +optional
	Schedule string `json:"schedule,omitempty"`
}

type RegistrySecretKeyRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
}

type RegistryWebhook struct {
	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	// +kubebuilder:validation:MaxLength=2048
	URL string `json:"url"`
	// +optional
	AuthHeaderSecretRef *RegistrySecretKeyRef `json:"authHeaderSecretRef,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.name) == has(oldSelf.name) && (!has(self.name) || self.name == oldSelf.name)",message="name is immutable"
type RegistryProjectSpec struct {
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]{1,28}[a-z0-9]$`
	// +optional
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	QuotaGB *int64 `json:"quotaGB,omitempty"`
	// +optional
	Retention *RegistryRetention `json:"retention,omitempty"`
	// +optional
	Webhook *RegistryWebhook `json:"webhook,omitempty"`
	// +optional
	ForceDestroy bool `json:"forceDestroy,omitempty"`
}

type RegistryStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	Host string `json:"host,omitempty"`
	// +optional
	ProjectName string `json:"projectName,omitempty"`
	// +optional
	ProjectID int64 `json:"projectID,omitempty"`
	// +optional
	UsedBytes int64 `json:"usedBytes,omitempty"`
	// +optional
	HardBytes int64 `json:"hardBytes,omitempty"`
	// +optional
	RepositoryCount int64 `json:"repositoryCount,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.status.host`
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.status.projectName`
// +kubebuilder:printcolumn:name="Used",type=integer,JSONPath=`.status.usedBytes`
// +kubebuilder:printcolumn:name="Repositories",type=integer,JSONPath=`.status.repositoryCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type Registry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RegistryProjectSpec `json:"spec"`
	Status            RegistryStatus      `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Registry `json:"items"`
}

type RegistryReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; create a new credential"
type RegistryCredentialSpec struct {
	RegistryRef RegistryReference `json:"registryRef"`
	// +kubebuilder:validation:Enum=pull;pushPull;manage
	Permission string `json:"permission"`
}

type RegistryCredentialStatus struct {
	CommonStatus `json:",inline"`
	// +optional
	SecretName string `json:"secretName,omitempty"`
	// +optional
	Username string `json:"username,omitempty"`
	// +optional
	RobotName string `json:"robotName,omitempty"`
	// +optional
	RobotID int64 `json:"robotID,omitempty"`
	// +optional
	SecretReset string `json:"secretReset,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Registry",type=string,JSONPath=`.spec.registryRef.name`
// +kubebuilder:printcolumn:name="Permission",type=string,JSONPath=`.spec.permission`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type RegistryCredential struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RegistryCredentialSpec   `json:"spec"`
	Status            RegistryCredentialStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RegistryCredentialList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RegistryCredential `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Registry{}, &RegistryList{}, &RegistryCredential{}, &RegistryCredentialList{})
}
