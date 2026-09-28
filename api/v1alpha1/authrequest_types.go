package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type LoginState struct {
	// +optional
	Step string `json:"step,omitempty"`
	// +optional
	Username string `json:"username,omitempty"`
	// +optional
	Provider string `json:"provider,omitempty"`
	// +optional
	Primary string `json:"primary,omitempty"`
	// +optional
	// +listType=set
	Completed []string `json:"completed,omitempty"`
	// +optional
	// +listType=set
	Required []string `json:"required,omitempty"`
	// +optional
	CSRFHash string `json:"csrfHash,omitempty"`
	// +optional
	Upstream string `json:"upstream,omitempty"`
	// +optional
	Error string `json:"error,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type AuthRequestSpec struct {
	ClientID string `json:"clientID"`
	// +optional
	RedirectURI string `json:"redirectURI,omitempty"`
	// +optional
	// +listType=set
	Scopes []string `json:"scopes,omitempty"`
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	Nonce string `json:"nonce,omitempty"`
	// +optional
	ResponseType string `json:"responseType,omitempty"`
	// +optional
	ResponseMode string `json:"responseMode,omitempty"`
	// +optional
	CodeChallenge string `json:"codeChallenge,omitempty"`
	// +optional
	CodeChallengeMethod string `json:"codeChallengeMethod,omitempty"`
	// +optional
	// +listType=set
	Prompt []string `json:"prompt,omitempty"`
	// +optional
	MaxAge *int64 `json:"maxAge,omitempty"`
	// +optional
	LoginHint string `json:"loginHint,omitempty"`
	// +optional
	// +listType=set
	UILocales []string `json:"uiLocales,omitempty"`
	// +optional
	DeviceRequest string      `json:"deviceRequest,omitempty"`
	ExpiresAt     metav1.Time `json:"expiresAt"`
}

type AuthRequestStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Login LoginState `json:"login,omitempty"`
	// +optional
	CookieHash string `json:"cookieHash,omitempty"`
	// +optional
	Subject string `json:"subject,omitempty"`
	// +optional
	AuthTime *metav1.Time `json:"authTime,omitempty"`
	// +optional
	// +listType=set
	AMR []string `json:"amr,omitempty"`
	// +optional
	Done bool `json:"done,omitempty"`
	// +optional
	Session string `json:"session,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
type AuthRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AuthRequestSpec   `json:"spec"`
	Status            AuthRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type AuthRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuthRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AuthRequest{}, &AuthRequestList{})
}
