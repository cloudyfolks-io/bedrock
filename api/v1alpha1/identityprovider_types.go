package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	IdentityProviderTypeLDAP = "ldap"
	IdentityProviderTypeOIDC = "oidc"
)

type LDAPUserSearch struct {
	BaseDN string `json:"baseDN"`
	// +optional
	Filter            string `json:"filter,omitempty"`
	UsernameAttribute string `json:"usernameAttribute"`
	// +optional
	DisplayNameAttribute string `json:"displayNameAttribute,omitempty"`
	// +optional
	EmailAttribute string `json:"emailAttribute,omitempty"`
}

type LDAPGroupSearch struct {
	BaseDN string `json:"baseDN"`
	// +optional
	Filter          string `json:"filter,omitempty"`
	MemberAttribute string `json:"memberAttribute"`
	NameAttribute   string `json:"nameAttribute"`
}

type LDAPProvider struct {
	URL string `json:"url"`
	// +optional
	StartTLS   bool           `json:"startTLS,omitempty"`
	BindDN     string         `json:"bindDN"`
	UserSearch LDAPUserSearch `json:"userSearch"`
	// +optional
	GroupSearch *LDAPGroupSearch `json:"groupSearch,omitempty"`
	// +optional
	MemberOfAttribute string `json:"memberOfAttribute,omitempty"`
}

type OIDCProvider struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientID"`
	// +optional
	// +listType=set
	Scopes []string `json:"scopes,omitempty"`
	// +optional
	UsernameClaim string `json:"usernameClaim,omitempty"`
	// +optional
	EmailClaim string `json:"emailClaim,omitempty"`
	// +optional
	NameClaim string `json:"nameClaim,omitempty"`
	// +optional
	GroupsClaim string `json:"groupsClaim,omitempty"`
}

type GroupMapping struct {
	External string `json:"external"`
	Group    string `json:"group"`
}

// +kubebuilder:validation:XValidation:rule="(self.type == 'ldap') == has(self.ldap)",message="ldap is set exactly when type is ldap"
// +kubebuilder:validation:XValidation:rule="(self.type == 'oidc') == has(self.oidc)",message="oidc is set exactly when type is oidc"
type IdentityProviderSpec struct {
	// +kubebuilder:validation:Enum=ldap;oidc
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="type is immutable"
	Type string `json:"type"`
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	LDAP *LDAPProvider `json:"ldap,omitempty"`
	// +optional
	OIDC *OIDCProvider `json:"oidc,omitempty"`
	// +optional
	GroupMapping []GroupMapping `json:"groupMapping,omitempty"`
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
	// +optional
	CABundle string `json:"caBundle,omitempty"`
	// +optional
	Disabled bool `json:"disabled,omitempty"`
}

type IdentityProviderStatus struct {
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
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
type IdentityProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              IdentityProviderSpec   `json:"spec"`
	Status            IdentityProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type IdentityProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IdentityProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IdentityProvider{}, &IdentityProviderList{})
}
