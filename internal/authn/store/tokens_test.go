package store

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type rotation struct {
	token string
	err   error
}

func loginRequest(subject, clientID string) AuthRequest {
	return AuthRequest{Object: v1alpha1.AuthRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "request-id"},
		Spec:       v1alpha1.AuthRequestSpec{ClientID: clientID, Scopes: []string{oidc.ScopeOpenID, oidc.ScopeOfflineAccess}, ResponseType: "code"},
		Status:     v1alpha1.AuthRequestStatus{Subject: subject, AMR: []string{"pwd", "otp"}, AuthTime: &metav1.Time{Time: testNow.Add(-time.Minute)}, Session: "session-name", Done: true},
	}}
}

func issue(t *testing.T, s *Store, request op.TokenRequest) string {
	t.Helper()
	_, token, _, err := s.CreateAccessAndRefreshTokens(context.Background(), request, "")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func refresh(ctx context.Context, s *Store, token string) (string, error) {
	request, err := s.TokenRequestByRefreshToken(ctx, token)
	if err != nil {
		return "", err
	}
	_, next, _, err := s.CreateAccessAndRefreshTokens(ctx, request, token)
	return next, err
}

func storedRefresh(t *testing.T, c client.Client, token string) v1alpha1.RefreshToken {
	t.Helper()
	var stored v1alpha1.RefreshToken
	if err := c.Get(context.Background(), objectKey(secret.SHA256Hex(token)), &stored); err != nil {
		t.Fatalf("refresh token object: %v", err)
	}
	return stored
}

func familyTokens(t *testing.T, c client.Client, family string) []v1alpha1.RefreshToken {
	t.Helper()
	var list v1alpha1.RefreshTokenList
	if err := c.List(context.Background(), &list, client.InNamespace(release.SystemNamespace), client.MatchingLabels{v1alpha1.LabelFamily: family}); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestRefreshRotates(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	accessID, first, expiry, err := s.CreateAccessAndRefreshTokens(ctx, loginRequest("alice", "bedrock-cli"), "")
	if err != nil {
		t.Fatal(err)
	}
	accessScopes, ok := scopesOfAccessTokenID(accessID)
	if !ok || !reflect.DeepEqual(accessScopes, []string{oidc.ScopeOpenID}) || len(first) != 43 || !expiry.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("access id %q, refresh token length %d, expiry %v", accessID, len(first), expiry)
	}
	stored := storedRefresh(t, c, first)
	if stored.Name != secret.SHA256Hex(first) || stored.Labels[v1alpha1.LabelFamily] != stored.Spec.Family || len(stored.Spec.Family) != 22 || len(stored.Labels[v1alpha1.LabelName]) != 63 {
		t.Fatalf("stored token metadata %+v family %q", stored.ObjectMeta, stored.Spec.Family)
	}
	if !stored.Spec.AuthTime.Time.Equal(testNow.Add(-time.Minute)) || !stored.Spec.ExpiresAt.Time.Equal(testNow.Add(720*time.Hour)) {
		t.Fatalf("auth time %v, expiry %v", stored.Spec.AuthTime, stored.Spec.ExpiresAt)
	}
	gotSpec := stored.Spec
	gotSpec.AuthTime, gotSpec.ExpiresAt = metav1.Time{}, metav1.Time{}
	wantSpec := v1alpha1.RefreshTokenSpec{
		Family:   stored.Spec.Family,
		UserRef:  "alice",
		ClientID: "bedrock-cli",
		Scopes:   []string{"openid", "offline_access"},
		Audience: []string{"bedrock", "bedrock-cli"},
		AMR:      []string{"pwd", "otp"},
		Session:  "session-name",
	}
	if !reflect.DeepEqual(gotSpec, wantSpec) || stored.Status.UsedAt != nil {
		t.Fatalf("stored spec %+v status %+v", stored.Spec, stored.Status)
	}
	request, err := s.TokenRequestByRefreshToken(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if request.GetSubject() != "alice" || request.GetClientID() != "bedrock-cli" || !request.GetAuthTime().Equal(testNow.Add(-time.Minute)) || sessionOf(request) != "session-name" {
		t.Fatalf("refresh request %+v", request)
	}
	request.SetCurrentScopes([]string{oidc.ScopeOpenID})
	_, second, _, err := s.CreateAccessAndRefreshTokens(ctx, request, first)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("rotation must give a new token")
	}
	used := storedRefresh(t, c, first)
	if used.Status.UsedAt == nil || !used.Status.UsedAt.Time.Equal(testNow) {
		t.Fatalf("the old token must be marked used, got %+v", used.Status)
	}
	next := storedRefresh(t, c, second)
	if next.Spec.Family != stored.Spec.Family || !reflect.DeepEqual(next.Spec.Scopes, []string{"openid"}) || next.Spec.Session != "session-name" || !reflect.DeepEqual(next.Spec.AMR, stored.Spec.AMR) {
		t.Fatalf("rotated token %+v", next.Spec)
	}
}

func TestRefreshReuseRevokesTheFamily(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	other := issue(t, s, loginRequest("alice", "bedrock-cli"))
	first := issue(t, s, loginRequest("alice", "bedrock-cli"))
	family := storedRefresh(t, c, first).Spec.Family
	second, err := refresh(ctx, s, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refresh(ctx, s, first); !errors.Is(err, errRefreshReused) {
		t.Fatalf("a reused token must fail as reused, got %v", err)
	}
	if left := familyTokens(t, c, family); len(left) != 0 {
		t.Fatalf("the family must be deleted, %d tokens left", len(left))
	}
	if _, err := refresh(ctx, s, second); !isNotFound(err) {
		t.Fatalf("the newest token of a revoked family must stop working, got %v", err)
	}
	if _, err := refresh(ctx, s, other); err != nil {
		t.Fatalf("another family must keep working: %v", err)
	}
}

func TestRefreshRotationIsAtomic(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	for round := range 5 {
		token := issue(t, s, loginRequest("alice", "bedrock-cli"))
		family := storedRefresh(t, c, token).Spec.Family
		requests := make([]op.RefreshTokenRequest, 0, 2)
		for range 2 {
			request, err := s.TokenRequestByRefreshToken(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			requests = append(requests, request)
		}
		start := make(chan struct{})
		results := make(chan rotation, len(requests))
		var wg sync.WaitGroup
		for _, request := range requests {
			wg.Add(1)
			go func(request op.RefreshTokenRequest) {
				defer wg.Done()
				<-start
				_, next, _, err := s.CreateAccessAndRefreshTokens(ctx, request, token)
				results <- rotation{token: next, err: err}
			}(request)
		}
		close(start)
		wg.Wait()
		close(results)
		failures := 0
		for result := range results {
			if result.err != nil {
				if !errors.Is(result.err, errRefreshReused) {
					t.Fatalf("round %d: a lost race must fail as reuse, got %v", round, result.err)
				}
				failures++
				continue
			}
			if _, err := s.TokenRequestByRefreshToken(ctx, result.token); err == nil {
				t.Fatalf("round %d: a token from a raced rotation must not work", round)
			}
		}
		if failures == 0 {
			t.Fatalf("round %d: only one rotation may win the update", round)
		}
		if left := familyTokens(t, c, family); len(left) != 0 {
			t.Fatalf("round %d: the family must be revoked, %d tokens left", round, len(left))
		}
	}
}

func TestRefreshRotationRejectsAStaleRead(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	token := issue(t, s, loginRequest("alice", "bedrock-cli"))
	family := storedRefresh(t, c, token).Spec.Family

	hook := &hookClient{Client: c}
	hook.afterGet = func() {
		if _, err := refresh(ctx, s, token); err != nil {
			t.Fatal(err)
		}
	}
	stale := New(Config{
		Client:   c,
		Reader:   hook,
		Random:   rand.Reader,
		Clock:    func() time.Time { return testNow },
		Settings: func(context.Context) (policy.Settings, error) { return testSettings(), nil },
	})
	request, err := stale.TokenRequestByRefreshToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := stale.CreateAccessAndRefreshTokens(ctx, request, token); !errors.Is(err, errRefreshReused) {
		t.Fatalf("a rotation built from a stale read must fail as reused, got %v", err)
	}
	if left := familyTokens(t, c, family); len(left) != 0 {
		t.Fatalf("the family must be revoked, %d tokens left", len(left))
	}
}

func TestRefreshRefusesDisabledUser(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"), testUser("bob"))
	s := newTestStore(c, testNow, testSettings(), nil)
	aliceToken := issue(t, s, loginRequest("alice", "bedrock-cli"))
	bobToken := issue(t, s, loginRequest("bob", "bedrock-cli"))
	var alice v1alpha1.User
	if err := c.Get(ctx, objectKey("alice"), &alice); err != nil {
		t.Fatal(err)
	}
	alice.Spec.Disabled = true
	if err := c.Update(ctx, &alice); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenRequestByRefreshToken(ctx, aliceToken); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("a disabled user must not refresh, got %v", err)
	}
	if err := c.Delete(ctx, testUser("bob")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenRequestByRefreshToken(ctx, bobToken); !apierrors.IsNotFound(err) {
		t.Fatalf("a deleted user must not refresh, got %v", err)
	}
}

func TestRefreshUsesSettingTTL(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	settings := testSettings()
	settings.RefreshTTL = 2 * time.Hour
	s := newTestStore(c, testNow, settings, nil)
	token := issue(t, s, loginRequest("alice", "bedrock-cli"))
	if got := storedRefresh(t, c, token).Spec.ExpiresAt.Time; !got.Equal(testNow.Add(2 * time.Hour)) {
		t.Fatalf("expiry %v, want the Setting's two hours", got)
	}
	late := newTestStore(c, testNow.Add(2*time.Hour), settings, nil)
	if _, err := late.TokenRequestByRefreshToken(ctx, token); !isNotFound(err) {
		t.Fatalf("an expired refresh token must be not found, got %v", err)
	}
	if _, _, err := late.GetRefreshTokenInfo(ctx, "bedrock-cli", token); !errors.Is(err, op.ErrInvalidRefreshToken) {
		t.Fatalf("an expired refresh token is no refresh token, got %v", err)
	}
}

func TestRevokeRefreshToken(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	token := issue(t, s, loginRequest("alice", "bedrock-cli"))
	name := storedRefresh(t, c, token).Name
	user, id, err := s.GetRefreshTokenInfo(ctx, "bedrock-cli", token)
	if err != nil || user != "alice" || id != name {
		t.Fatalf("info user %q id %q err %v", user, id, err)
	}
	for _, tc := range []struct{ clientID, token string }{{"other-client", token}, {"bedrock-cli", "not-a-refresh-token"}} {
		if _, _, err := s.GetRefreshTokenInfo(ctx, tc.clientID, tc.token); !errors.Is(err, op.ErrInvalidRefreshToken) {
			t.Fatalf("client %q token %q: want ErrInvalidRefreshToken, got %v", tc.clientID, tc.token, err)
		}
	}
	if err := s.RevokeToken(ctx, id, "alice", "other-client"); err == nil || err.ErrorType != oidc.InvalidClient {
		t.Fatalf("another client must not revoke the token, got %v", err)
	}
	if err := s.RevokeToken(ctx, name, "", "bedrock-cli"); err != nil {
		t.Fatalf("an unknown raw value is accepted: %v", err)
	}
	storedRefresh(t, c, token)
	if err := s.RevokeToken(ctx, id, "alice", "bedrock-cli"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.TokenRequestByRefreshToken(ctx, token); !isNotFound(err) {
		t.Fatalf("a revoked token must be not found, got %v", err)
	}
	raw := issue(t, s, loginRequest("alice", "bedrock-cli"))
	if err := s.RevokeToken(ctx, raw, "", "bedrock-cli"); err != nil {
		t.Fatalf("revoke by raw value: %v", err)
	}
	if _, err := s.TokenRequestByRefreshToken(ctx, raw); !isNotFound(err) {
		t.Fatalf("a token revoked by its raw value must be not found, got %v", err)
	}
	if err := s.RevokeToken(ctx, "access-token-id", "alice", "bedrock-cli"); err != nil {
		t.Fatalf("an access token ID is accepted without effect: %v", err)
	}
}

func TestTerminateSession(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	cliToken := issue(t, s, loginRequest("alice", "bedrock-cli"))
	consoleToken := issue(t, s, loginRequest("alice", "console"))
	cookie, err := s.CreateSession(ctx, methods.Subject{User: *testUser("alice"), AMR: []string{"pwd"}}, "browser", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TerminateSession(ctx, "alice", "console"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenRequestByRefreshToken(ctx, consoleToken); !isNotFound(err) {
		t.Fatalf("the client's refresh token must be gone, got %v", err)
	}
	if _, err := s.TokenRequestByRefreshToken(ctx, cliToken); err != nil {
		t.Fatalf("another client's refresh token must stay: %v", err)
	}
	if _, err := s.SessionByCookie(ctx, cookie); !isNotFound(err) {
		t.Fatalf("the user's sessions must end, got %v", err)
	}
}
