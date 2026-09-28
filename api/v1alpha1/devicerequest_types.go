package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DeviceStatePending  = "Pending"
	DeviceStateApproved = "Approved"
	DeviceStateDenied   = "Denied"
)

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type DeviceRequestSpec struct {
	ClientID string `json:"clientID"`
	// +optional
	// +listType=set
	Scopes []string `json:"scopes,omitempty"`
	// +optional
	// +listType=set
	Audience     []string    `json:"audience,omitempty"`
	UserCodeHash string      `json:"userCodeHash"`
	ExpiresAt    metav1.Time `json:"expiresAt"`
}

type DeviceRequestStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:validation:Enum=Pending;Approved;Denied
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	Subject string `json:"subject,omitempty"`
	// +optional
	// +listType=set
	AMR []string `json:"amr,omitempty"`
	// +optional
	AuthTime *metav1.Time `json:"authTime,omitempty"`
	// +optional
	LastPoll *metav1.Time `json:"lastPoll,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:selectablefield:JSONPath=`.spec.userCodeHash`
type DeviceRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceRequestSpec   `json:"spec"`
	Status            DeviceRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DeviceRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeviceRequest{}, &DeviceRequestList{})
}
