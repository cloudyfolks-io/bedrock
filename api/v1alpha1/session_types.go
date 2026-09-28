package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type SessionSpec struct {
	UserRef string `json:"userRef"`
	// +optional
	// +listType=set
	AMR      []string    `json:"amr,omitempty"`
	AuthTime metav1.Time `json:"authTime"`
	// +optional
	UserAgent string `json:"userAgent,omitempty"`
	// +optional
	ClientIP  string      `json:"clientIP,omitempty"`
	ExpiresAt metav1.Time `json:"expiresAt"`
}

type SessionStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	LastSeen *metav1.Time `json:"lastSeen,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:selectablefield:JSONPath=`.spec.userRef`
type Session struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SessionSpec   `json:"spec"`
	Status            SessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Session `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Session{}, &SessionList{})
}
