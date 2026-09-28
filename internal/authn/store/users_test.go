package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type identityRequest struct {
	subject string
	amr     []string
	session string
}

func (r identityRequest) GetAMR() []string       { return r.amr }
func (r identityRequest) GetAudience() []string  { return []string{"bedrock", "bedrock-cli"} }
func (r identityRequest) GetAuthTime() time.Time { return testNow }
func (r identityRequest) GetClientID() string    { return "bedrock-cli" }
func (r identityRequest) GetScopes() []string    { return []string{oidc.ScopeOpenID} }
func (r identityRequest) GetSubject() string     { return r.subject }
func (r identityRequest) sessionID() string      { return r.session }

func aliceUser() v1alpha1.User {
	return v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec:       v1alpha1.UserSpec{Username: "alice", DisplayName: "Alice Example", Email: "alice@example.test"},
	}
}

func TestClaims(t *testing.T) {
	got := Claims(aliceUser(), []string{"admins", "ops"}, []string{"pwd", "otp"}, "s1")
	want := map[string]any{
		"groups": []string{"admins", "ops"},
		"email":  "alice@example.test",
		"name":   "Alice Example",
		"amr":    []string{"pwd", "otp"},
		"sid":    "s1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claims %v, want %v", got, want)
	}
	bare := Claims(v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "bob"}}, nil, nil, "")
	if !reflect.DeepEqual(bare, map[string]any{"groups": []string{}}) {
		t.Fatalf("a user without email, name, amr or session must carry only groups, got %v", bare)
	}
}

func TestUserinfoByScope(t *testing.T) {
	cases := []struct {
		scopes []string
		want   oidc.UserInfo
	}{
		{[]string{oidc.ScopeOpenID}, oidc.UserInfo{Subject: "alice"}},
		{[]string{oidc.ScopeOpenID, oidc.ScopeProfile}, oidc.UserInfo{Subject: "alice", UserInfoProfile: oidc.UserInfoProfile{Name: "Alice Example", PreferredUsername: "alice"}}},
		{[]string{oidc.ScopeOpenID, oidc.ScopeEmail}, oidc.UserInfo{Subject: "alice", UserInfoEmail: oidc.UserInfoEmail{Email: "alice@example.test"}}},
		{[]string{oidc.ScopeOpenID, "groups"}, oidc.UserInfo{Subject: "alice", Claims: map[string]any{"groups": []string{"ops"}}}},
	}
	for _, tc := range cases {
		got := Userinfo(aliceUser(), []string{"ops"}, tc.scopes)
		if !reflect.DeepEqual(*got, tc.want) {
			t.Fatalf("scopes %v: userinfo %+v, want %+v", tc.scopes, *got, tc.want)
		}
		if got.EmailVerified {
			t.Fatalf("scopes %v: email_verified must stay false", tc.scopes)
		}
	}
}

func TestUserByUsernameNormalizes(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	email := "t.farahani@example.test"
	emailUser := testUser(v1alpha1.UserObjectName(email))
	emailUser.Spec.Username = email
	create(t, c, testUser("alice"), emailUser)
	s := newTestStore(c, testNow, testSettings(), nil)
	for _, raw := range []string{"alice", "Alice", " alice "} {
		user, err := s.UserByUsername(ctx, raw)
		if err != nil || user.Name != "alice" {
			t.Fatalf("%q: user %q, err %v", raw, user.Name, err)
		}
	}
	user, err := s.UserByUsername(ctx, " T.Farahani@Example.TEST")
	if err != nil || user.Name != v1alpha1.UserObjectName(email) {
		t.Fatalf("email user %q, err %v", user.Name, err)
	}
	if _, err := s.UserByUsername(ctx, "bob"); !apierrors.IsNotFound(err) {
		t.Fatalf("an unknown user must be not found, got %v", err)
	}
	if _, err := s.UserByUsername(ctx, "al*ce"); err == nil {
		t.Fatal("an invalid username must fail")
	}
}

func TestSubjectHasEffectiveGroups(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	alice := testUser("alice")
	alice.Spec.Groups = []string{"ops"}
	carol := testUser("carol")
	carol.Spec.Disabled = true
	create(t, c, alice, carol, testGroup("admins", "alice"), testGroup("dev", "bob"))
	s := newTestStore(c, testNow, testSettings(), nil)

	subject, err := s.Subject(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(subject.Groups, []string{"admins", "ops"}) || subject.User.Name != "alice" {
		t.Fatalf("subject %+v", subject)
	}
	if _, err := s.Subject(ctx, "carol"); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("a disabled user must fail with ErrUserDisabled, got %v", err)
	}

	request := identityRequest{subject: "alice", amr: []string{"pwd", "otp"}, session: "s1"}
	claims, err := s.GetPrivateClaimsFromRequest(ctx, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(claims, Claims(subject.User, []string{"admins", "ops"}, []string{"pwd", "otp"}, "s1")) {
		t.Fatalf("request claims %v", claims)
	}
	scoped, err := s.GetPrivateClaimsFromScopes(ctx, "alice", "bedrock-cli", []string{oidc.ScopeOpenID})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := scoped["amr"]; ok {
		t.Fatalf("scope claims know no amr, got %v", scoped)
	}
	if _, err := s.GetPrivateClaimsFromScopes(ctx, "carol", "bedrock-cli", nil); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("claims of a disabled user must fail, got %v", err)
	}

	info := &oidc.UserInfo{}
	if err := s.SetUserinfoFromRequest(ctx, info, request, []string{oidc.ScopeOpenID, oidc.ScopeProfile}); err != nil {
		t.Fatal(err)
	}
	if info.Subject != "alice" || info.PreferredUsername != "alice" || info.Claims["sid"] != "s1" || !reflect.DeepEqual(info.Claims["groups"], []string{"admins", "ops"}) {
		t.Fatalf("id token userinfo %+v", info)
	}
	full := &oidc.UserInfo{}
	fullScopeToken := accessTokenID("abcdefghijklmnopqrstuv", []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail, scopeGroups})
	if err := s.SetUserinfoFromToken(ctx, full, fullScopeToken, "alice", ""); err != nil {
		t.Fatal(err)
	}
	if full.Email != "alice@example.test" || full.Name != "Test alice" || !reflect.DeepEqual(full.Claims["groups"], []string{"admins", "ops"}) {
		t.Fatalf("userinfo endpoint answer %+v", full)
	}
}

func TestSetUserinfoFromTokenIsScopedByTheAccessTokenID(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	openIDOnly := accessTokenID("abcdefghijklmnopqrstuv", []string{oidc.ScopeOpenID})
	cases := map[string]string{
		"openid-only scopes":  openIDOnly,
		"no separator":        "not-an-access-token-id",
		"21-character prefix": "abcdefghijklmnopqrstu.oeg",
		"23-character prefix": "abcdefghijklmnopqrstuvw.oeg",
		"unknown code byte":   "abcdefghijklmnopqrstuv.ox",
		"duplicate code byte": "abcdefghijklmnopqrstuv.oo",
	}
	for name, tokenID := range cases {
		info := &oidc.UserInfo{}
		if err := s.SetUserinfoFromToken(ctx, info, tokenID, "alice", ""); err != nil {
			t.Fatal(err)
		}
		if info.Subject != "alice" || info.Email != "" || info.Name != "" || info.PreferredUsername != "" || info.Claims["groups"] != nil {
			t.Fatalf("%s (%q): an openid-only access token must carry no profile, email or groups, got %+v", name, tokenID, info)
		}
	}
}
