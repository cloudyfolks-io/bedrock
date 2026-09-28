package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type TokenExchange struct {
	// +listType=set
	Audiences []string `json:"audiences"`
}

// +kubebuilder:validation:XValidation:rule="!self.public || size(self.secretRef) == 0",message="a public client has no secretRef"
// +kubebuilder:validation:XValidation:rule="self.public || size(self.secretRef) != 0",message="a confidential client requires secretRef"
type OAuthClientSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clientID is immutable"
	ClientID string `json:"clientID"`
	// +optional
	// +kubebuilder:default=false
	Public bool `json:"public,omitempty"`
	// +optional
	// +listType=set
	RedirectURIs []string `json:"redirectURIs,omitempty"`
	// +optional
	// +listType=set
	// +kubebuilder:validation:items:Enum=authorization_code;refresh_token;"urn:ietf:params:oauth:grant-type:device_code";"urn:ietf:params:oauth:grant-type:token-exchange"
	GrantTypes []string `json:"grantTypes,omitempty"`
	// +optional
	RequireSecondFactor bool `json:"requireSecondFactor,omitempty"`
	// +optional
	TokenExchange *TokenExchange `json:"tokenExchange,omitempty"`
	// +optional
	// +kubebuilder:default=""
	SecretRef string `json:"secretRef,omitempty"`
}

type OAuthClientStatus struct {
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
// +kubebuilder:printcolumn:name="Public",type=boolean,JSONPath=`.spec.public`
type OAuthClient struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              OAuthClientSpec   `json:"spec"`
	Status            OAuthClientStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OAuthClientList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OAuthClient `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OAuthClient{}, &OAuthClientList{})
}
