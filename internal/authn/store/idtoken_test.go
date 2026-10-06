package store

import (
	"reflect"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestIDTokenAudienceIsTheClient(t *testing.T) {
	login := v1alpha1.AuthRequest{
		Spec:   v1alpha1.AuthRequestSpec{ClientID: "bedrock-cli", Nonce: "nonce-value"},
		Status: v1alpha1.AuthRequestStatus{Subject: "alice", Session: "session-hash"},
	}
	refresh := &RefreshTokenRequest{Object: v1alpha1.RefreshToken{Spec: v1alpha1.RefreshTokenSpec{ClientID: "bedrock-cli", UserRef: "alice", Session: "session-hash", Audience: []string{"bedrock", "bedrock-cli"}}}}
	device := &op.DeviceAuthorizationState{ClientID: "bedrock-cli", Subject: "alice", Audience: []string{"bedrock", "bedrock-cli"}}
	requests := map[string]op.IDTokenRequest{"login": AuthRequest{Object: login}, "refresh": refresh, "device": device}
	for name, request := range requests {
		if got := request.GetAudience(); !reflect.DeepEqual(got, []string{"bedrock", "bedrock-cli"}) {
			t.Fatalf("%s access token audience %v", name, got)
		}
		wrapped := ForIDToken(request)
		if got := wrapped.GetAudience(); !reflect.DeepEqual(got, []string{"bedrock-cli"}) {
			t.Fatalf("%s ID token audience %v", name, got)
		}
		if wrapped.GetSubject() != "alice" || wrapped.GetClientID() != "bedrock-cli" {
			t.Fatalf("%s identity %s %s", name, wrapped.GetSubject(), wrapped.GetClientID())
		}
	}
	wrappedLogin, ok := ForIDToken(AuthRequest{Object: login}).(op.AuthRequest)
	if !ok || wrappedLogin.GetNonce() != "nonce-value" {
		t.Fatal("the ID token request of a login must keep the nonce")
	}
	for name, request := range map[string]op.IDTokenRequest{"login": AuthRequest{Object: login}, "refresh": refresh} {
		if sid := sessionOf(ForIDToken(request)); sid != "session-hash" {
			t.Fatalf("%s session %q", name, sid)
		}
	}
}
