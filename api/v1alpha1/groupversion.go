// +kubebuilder:object:generate=true
// +groupName=bedrock.cloudyfolks.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "bedrock.cloudyfolks.io", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)

const (
	LabelKind = "bedrock.cloudyfolks.io/kind"
	LabelName = "bedrock.cloudyfolks.io/name"

	LabelAuthn        = "bedrock.cloudyfolks.io/authn"
	LabelFamily       = "bedrock.cloudyfolks.io/family"
	AuthnFieldManager = "bedrock-authn"
	AuthnPrefix       = "bedrock:"
	GroupAdmins       = "bedrock-admins"
	UserAdmin         = "admin"

	GrantAuthorizationCode = "authorization_code"
	GrantRefreshToken      = "refresh_token"
	GrantDeviceCode        = "urn:ietf:params:oauth:grant-type:device_code"
	GrantTokenExchange     = "urn:ietf:params:oauth:grant-type:token-exchange"

	MethodPassword = "password"
	MethodTOTP     = "totp"
	MethodRecovery = "recovery"
	MethodLDAP     = "ldap"
	MethodOIDC     = "oidc"
)
