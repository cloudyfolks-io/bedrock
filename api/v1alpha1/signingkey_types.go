package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const AlgorithmES256 = "ES256"

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type SigningKeySpec struct {
	// +kubebuilder:validation:Enum=ES256
	Algorithm   string      `json:"algorithm"`
	SecretRef   string      `json:"secretRef"`
	NotBefore   metav1.Time `json:"notBefore"`
	RetireAfter metav1.Time `json:"retireAfter"`
}

type SigningKeyStatus struct {
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
type SigningKey struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SigningKeySpec   `json:"spec"`
	Status            SigningKeyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SigningKeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SigningKey `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SigningKey{}, &SigningKeyList{})
}

func SigningKeyName(notBefore time.Time) string {
	return "k-" + notBefore.UTC().Format("20060102t150405z")
}
