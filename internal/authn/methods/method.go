package methods

import (
	"context"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type Kind int

const (
	Primary Kind = iota + 1
	Second
)

type Subject struct {
	User   v1alpha1.User
	Groups []string
	AMR    []string
}

type Answer struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Code     string `json:"code,omitempty"`
	Provider string `json:"provider,omitempty"`
	Approve  *bool  `json:"approve,omitempty"`
	Method   string `json:"method,omitempty"`
}

type Flow struct {
	AuthRequest v1alpha1.AuthRequest
	ClientIP    string
	Now         time.Time
}

type Result struct {
	Subject   *Subject
	Challenge *Challenge
	Failure   string
}

type Method interface {
	Name() string
	Kind() Kind
	Begin(ctx context.Context, flow Flow, user v1alpha1.User) (Challenge, error)
	Complete(ctx context.Context, flow Flow, user v1alpha1.User, answer Answer) (Result, error)
	Enroll(ctx context.Context, user v1alpha1.User, input Answer) (Enrollment, error)
}

type Enrollment struct {
	Credential    v1alpha1.Credential
	TOTP          *TOTPEnrollment
	RecoveryCodes []string
}

type Registry map[string]Method

func NewRegistry(methods ...Method) Registry {
	registry := make(Registry, len(methods))
	for _, m := range methods {
		registry[m.Name()] = m
	}
	return registry
}

const (
	FailureInvalidCredentials = "invalid_credentials"
	FailureLocked             = "locked"
	FailureRateLimited        = "rate_limited"
	FailureInvalidCode        = "invalid_code"
	FailureProviderError      = "provider_error"
	FailureDisabled           = "disabled"
)

const maxLabelValue = 63

func labelValue(name string) string {
	return name[:min(len(name), maxLabelValue)]
}
