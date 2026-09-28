package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type RefreshTokenSpec struct {
	Family   string `json:"family"`
	UserRef  string `json:"userRef"`
	ClientID string `json:"clientID"`
	// +optional
	// +listType=set
	Scopes []string `json:"scopes,omitempty"`
	// +optional
	// +listType=set
	Audience []string `json:"audience,omitempty"`
	// +optional
	// +listType=set
	AMR      []string    `json:"amr,omitempty"`
	AuthTime metav1.Time `json:"authTime"`
	// +optional
	Session   string      `json:"session,omitempty"`
	ExpiresAt metav1.Time `json:"expiresAt"`
}

type RefreshTokenStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	UsedAt *metav1.Time `json:"usedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:selectablefield:JSONPath=`.spec.family`
// +kubebuilder:selectablefield:JSONPath=`.spec.userRef`
type RefreshToken struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RefreshTokenSpec   `json:"spec"`
	Status            RefreshTokenStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RefreshTokenList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RefreshToken `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RefreshToken{}, &RefreshTokenList{})
}
