package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

type UserSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="username is immutable"
	Username string `json:"username"`
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	Email string `json:"email,omitempty"`
	// +optional
	Disabled bool `json:"disabled,omitempty"`
	// +optional
	// +listType=set
	Groups []string `json:"groups,omitempty"`
	// +optional
	// +listType=set
	// +kubebuilder:validation:items:Enum=password;totp;recovery;ldap;oidc
	Methods []string `json:"methods,omitempty"`
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="source is immutable"
	Source string `json:"source,omitempty"`
}

type UserStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	LastLogin *metav1.Time `json:"lastLogin,omitempty"`
	// +optional
	LockedUntil *metav1.Time `json:"lockedUntil,omitempty"`
	// +optional
	FailedAttempts int32 `json:"failedAttempts,omitempty"`
	// +optional
	FailureWindowStart *metav1.Time `json:"failureWindowStart,omitempty"`
	// +optional
	Locks int32 `json:"locks,omitempty"`
	// +optional
	UpstreamSubject string `json:"upstreamSubject,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Username",type=string,JSONPath=`.spec.username`
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source`
// +kubebuilder:printcolumn:name="Disabled",type=boolean,JSONPath=`.spec.disabled`
type User struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UserSpec   `json:"spec"`
	Status            UserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type UserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []User `json:"items"`
}

func init() {
	SchemeBuilder.Register(&User{}, &UserList{})
}

func lowerASCII(raw string) string {
	out := []byte(raw)
	for i, b := range out {
		if b >= 'A' && b <= 'Z' {
			out[i] = b + ('a' - 'A')
		}
	}
	return string(out)
}

func NormalizeUsername(raw string) (string, error) {
	normalized := lowerASCII(strings.TrimSpace(raw))
	if len(validation.IsDNS1123Subdomain(normalized)) == 0 {
		return normalized, nil
	}
	if _, err := mail.ParseAddress(normalized); err == nil {
		return normalized, nil
	}
	return "", fmt.Errorf("username %q is not a DNS-1123 subdomain or an email address", raw)
}

func UserObjectName(username string) string {
	if len(username) <= 63 && len(validation.IsDNS1123Subdomain(username)) == 0 {
		return username
	}
	sum := sha256.Sum256([]byte(username))
	return "u-" + hex.EncodeToString(sum[:])[:20]
}

func CredentialName(user, method string) string {
	return user + "-" + method
}
