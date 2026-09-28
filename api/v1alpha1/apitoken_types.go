package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type APITokenSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="userRef is immutable"
	UserRef string `json:"userRef"`
	// +optional
	// +listType=set
	Scopes []string `json:"scopes,omitempty"`
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// +optional
	Description string `json:"description,omitempty"`
}

type APITokenStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	LastUsed *metav1.Time `json:"lastUsed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:selectablefield:JSONPath=`.spec.userRef`
type APIToken struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              APITokenSpec   `json:"spec"`
	Status            APITokenStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type APITokenList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []APIToken `json:"items"`
}

func init() {
	SchemeBuilder.Register(&APIToken{}, &APITokenList{})
}
