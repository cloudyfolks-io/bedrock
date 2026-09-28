package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CredentialSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="userRef is immutable"
	UserRef string `json:"userRef"`
	// +kubebuilder:validation:Enum=password;totp;recovery
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="method is immutable"
	Method string `json:"method"`
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

type CredentialStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	EnrolledAt *metav1.Time `json:"enrolledAt,omitempty"`
	// +optional
	LastUsed *metav1.Time `json:"lastUsed,omitempty"`
	// +optional
	LastStep int64 `json:"lastStep,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:selectablefield:JSONPath=`.spec.userRef`
type Credential struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CredentialSpec   `json:"spec"`
	Status            CredentialStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CredentialList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Credential `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Credential{}, &CredentialList{})
}
