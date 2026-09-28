package methods

type Challenge struct {
	Type          string           `json:"type"`
	CSRF          string           `json:"csrf,omitempty"`
	Username      string           `json:"username,omitempty"`
	Methods       []string         `json:"methods,omitempty"`
	Providers     []ProviderChoice `json:"providers,omitempty"`
	Redirect      string           `json:"redirect,omitempty"`
	Enroll        *TOTPEnrollment  `json:"enroll,omitempty"`
	RecoveryCodes []string         `json:"recoveryCodes,omitempty"`
	Device        *DeviceChallenge `json:"device,omitempty"`
	Error         *ChallengeError  `json:"error,omitempty"`
}

type ProviderChoice struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

type TOTPEnrollment struct {
	OTPAuthURL string `json:"otpauthURL"`
	Secret     string `json:"secret"`
}

type DeviceChallenge struct {
	UserCode string   `json:"userCode"`
	ClientID string   `json:"clientID"`
	Scopes   []string `json:"scopes"`
}

type ChallengeError struct {
	Code string `json:"code"`
}

const (
	ChallengeUsername      = "username"
	ChallengePassword      = "password"
	ChallengeTOTP          = "totp"
	ChallengeRecovery      = "recovery"
	ChallengeTOTPEnroll    = "totp-enroll"
	ChallengeProviders     = "providers"
	ChallengeRedirect      = "redirect"
	ChallengeDeviceConfirm = "device-confirm"
	ChallengeDone          = "done"
	ChallengeErrorType     = "error"
)
