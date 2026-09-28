package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func apiToken(user, value string) *v1alpha1.APIToken {
	return &v1alpha1.APIToken{
		ObjectMeta: metav1.ObjectMeta{Name: secret.SHA256Hex(value), Namespace: release.SystemNamespace},
		Spec:       v1alpha1.APITokenSpec{UserRef: user, Scopes: []string{"kubernetes"}, Description: "test token"},
	}
}

func TestSessionByCookie(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	cookie, err := s.CreateSession(ctx, methods.Subject{User: *testUser("alice"), AMR: []string{"pwd", "otp"}}, "curl/8.9", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if len(cookie) != 43 {
		t.Fatalf("cookie length %d, want 43", len(cookie))
	}
	var stored v1alpha1.Session
	if err := c.Get(ctx, objectKey(secret.SHA256Hex(cookie)), &stored); err != nil {
		t.Fatalf("the session must be stored under the cookie's SHA-256: %v", err)
	}
	if !stored.Spec.AuthTime.Time.Equal(testNow) || !stored.Spec.ExpiresAt.Time.Equal(testNow.Add(12*time.Hour)) {
		t.Fatalf("auth time %v, expiry %v", stored.Spec.AuthTime, stored.Spec.ExpiresAt)
	}
	gotSpec := stored.Spec
	gotSpec.AuthTime, gotSpec.ExpiresAt = metav1.Time{}, metav1.Time{}
	wantSpec := v1alpha1.SessionSpec{
		UserRef:   "alice",
		AMR:       []string{"pwd", "otp"},
		UserAgent: "curl/8.9",
		ClientIP:  "192.0.2.10",
	}
	if !reflect.DeepEqual(gotSpec, wantSpec) {
		t.Fatalf("session spec %+v", stored.Spec)
	}
	session, err := s.SessionByCookie(ctx, cookie)
	if err != nil || session.Name != stored.Name {
		t.Fatalf("session %q, err %v", session.Name, err)
	}
	if err := c.Get(ctx, objectKey(stored.Name), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.LastSeen == nil || !stored.Status.LastSeen.Time.Equal(testNow) {
		t.Fatalf("last seen %v, want %v", stored.Status.LastSeen, testNow)
	}
	if _, err := s.SessionByCookie(ctx, "wrong-cookie"); !isNotFound(err) {
		t.Fatalf("a wrong cookie must be not found, got %v", err)
	}
	late := newTestStore(c, testNow.Add(12*time.Hour), testSettings(), nil)
	if _, err := late.SessionByCookie(ctx, cookie); !isNotFound(err) {
		t.Fatalf("an expired session must be not found, got %v", err)
	}
	var alice v1alpha1.User
	if err := c.Get(ctx, objectKey("alice"), &alice); err != nil {
		t.Fatal(err)
	}
	alice.Spec.Disabled = true
	if err := c.Update(ctx, &alice); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByCookie(ctx, cookie); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("a disabled user's session must fail, got %v", err)
	}
}

func TestRevokeUser(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"), testUser("bob"), apiToken("alice", "brk_alice"), apiToken("bob", "brk_bob"))
	s := newTestStore(c, testNow, testSettings(), nil)
	aliceToken := issue(t, s, loginRequest("alice", "bedrock-cli"))
	bobToken := issue(t, s, loginRequest("bob", "bedrock-cli"))
	aliceCookie, err := s.CreateSession(ctx, methods.Subject{User: *testUser("alice")}, "browser", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	bobCookie, err := s.CreateSession(ctx, methods.Subject{User: *testUser("bob")}, "browser", "192.0.2.11")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenRequestByRefreshToken(ctx, aliceToken); !isNotFound(err) {
		t.Fatalf("alice's refresh token must be gone, got %v", err)
	}
	if _, err := s.SessionByCookie(ctx, aliceCookie); !isNotFound(err) {
		t.Fatalf("alice's session must be gone, got %v", err)
	}
	if err := c.Get(ctx, objectKey(secret.SHA256Hex("brk_alice")), &v1alpha1.APIToken{}); !isNotFound(err) {
		t.Fatalf("alice's API token must be gone, got %v", err)
	}
	if _, err := s.TokenRequestByRefreshToken(ctx, bobToken); err != nil {
		t.Fatalf("bob's refresh token must stay: %v", err)
	}
	if _, err := s.SessionByCookie(ctx, bobCookie); err != nil {
		t.Fatalf("bob's session must stay: %v", err)
	}
	if err := c.Get(ctx, objectKey(secret.SHA256Hex("brk_bob")), &v1alpha1.APIToken{}); err != nil {
		t.Fatalf("bob's API token must stay: %v", err)
	}
}
