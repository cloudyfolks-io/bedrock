package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type AuthCodeSpec struct {
	AuthRequest string      `json:"authRequest"`
	ExpiresAt   metav1.Time `json:"expiresAt"`
}

type AuthCodeStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
type AuthCode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AuthCodeSpec   `json:"spec"`
	Status            AuthCodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type AuthCodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuthCode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AuthCode{}, &AuthCodeList{})
}
