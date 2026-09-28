package methods

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestChallengeJSON(t *testing.T) {
	cases := []struct {
		name      string
		challenge Challenge
		want      string
	}{
		{
			"username",
			Challenge{Type: ChallengeUsername, CSRF: "csrf-1"},
			`{"type":"username","csrf":"csrf-1"}`,
		},
		{
			"password",
			Challenge{Type: ChallengePassword, CSRF: "csrf-1", Username: "alice"},
			`{"type":"password","csrf":"csrf-1","username":"alice"}`,
		},
		{
			"totp",
			Challenge{Type: ChallengeTOTP, CSRF: "csrf-1", Username: "alice"},
			`{"type":"totp","csrf":"csrf-1","username":"alice"}`,
		},
		{
			"recovery",
			Challenge{Type: ChallengeRecovery, CSRF: "csrf-1", Username: "alice"},
			`{"type":"recovery","csrf":"csrf-1","username":"alice"}`,
		},
		{
			"totp-enroll",
			Challenge{Type: ChallengeTOTPEnroll, CSRF: "csrf-1", Enroll: &TOTPEnrollment{OTPAuthURL: "otpauth://totp/sso.example.test:alice?secret=ABC", Secret: "ABC"}},
			`{"type":"totp-enroll","csrf":"csrf-1","enroll":{"otpauthURL":"otpauth://totp/sso.example.test:alice?secret=ABC","secret":"ABC"}}`,
		},
		{
			"providers",
			Challenge{Type: ChallengeProviders, CSRF: "csrf-1", Providers: []ProviderChoice{{Name: "corp-ldap", DisplayName: "Corp LDAP", Type: "ldap"}}},
			`{"type":"providers","csrf":"csrf-1","providers":[{"name":"corp-ldap","displayName":"Corp LDAP","type":"ldap"}]}`,
		},
		{
			"redirect",
			Challenge{Type: ChallengeRedirect, Redirect: "https://idp.example.test/authorize"},
			`{"type":"redirect","redirect":"https://idp.example.test/authorize"}`,
		},
		{
			"device-confirm",
			Challenge{Type: ChallengeDeviceConfirm, CSRF: "csrf-1", Device: &DeviceChallenge{UserCode: "ABCD-EFGH", ClientID: "bedrock-cli", Scopes: []string{"openid", "offline_access"}}},
			`{"type":"device-confirm","csrf":"csrf-1","device":{"userCode":"ABCD-EFGH","clientID":"bedrock-cli","scopes":["openid","offline_access"]}}`,
		},
		{
			"done",
			Challenge{Type: ChallengeDone, Redirect: "https://sso.example.test/oauth/v2/authorize?state=x"},
			`{"type":"done","redirect":"https://sso.example.test/oauth/v2/authorize?state=x"}`,
		},
		{
			"error",
			Challenge{Type: ChallengeErrorType, Error: &ChallengeError{Code: FailureInvalidCredentials}},
			`{"type":"error","error":{"code":"invalid_credentials"}}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.challenge)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("json.Marshal(%s) = %s, want %s", c.name, got, c.want)
			}
		})
	}
}

type fakeMethod struct{ name string }

func (f fakeMethod) Name() string { return f.name }
func (f fakeMethod) Kind() Kind   { return Primary }
func (f fakeMethod) Begin(ctx context.Context, flow Flow, user v1alpha1.User) (Challenge, error) {
	return Challenge{}, nil
}
func (f fakeMethod) Complete(ctx context.Context, flow Flow, user v1alpha1.User, answer Answer) (Result, error) {
	return Result{}, nil
}
func (f fakeMethod) Enroll(ctx context.Context, user v1alpha1.User, input Answer) (Enrollment, error) {
	return Enrollment{}, nil
}

func TestNewRegistryKeysByName(t *testing.T) {
	registry := NewRegistry(fakeMethod{name: "password"}, fakeMethod{name: "totp"})
	if len(registry) != 2 {
		t.Fatalf("len(registry) = %d, want 2", len(registry))
	}
	if registry["password"].Name() != "password" || registry["totp"].Name() != "totp" {
		t.Fatalf("registry keys do not match method names: %+v", registry)
	}
}
