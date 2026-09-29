package account

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type recoveryAnswer struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

func enrollTOTP(t *testing.T, h harness, cookie, csrf string) []string {
	t.Helper()
	enrollment := decode[methods.TOTPEnrollment](t, call(t, h, http.MethodPost, "/api/v1/account/totp", cookie, csrf, nil), http.StatusOK)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatal(err)
	}
	code := methods.TOTPCode(seed, methods.TOTPStep(time.Now()))
	return decode[recoveryAnswer](t, call(t, h, http.MethodPost, "/api/v1/account/totp/verify", cookie, csrf, map[string]string{"code": code}), http.StatusOK).RecoveryCodes
}

func TestAccountNeedsSession(t *testing.T) {
	h := newHarness(t)
	alice := createUser(t, h, "alice", "")
	bob := createUser(t, h, "bob", "")
	if code := errorOf(t, call(t, h, http.MethodGet, "/api/v1/account", "", "", nil), http.StatusUnauthorized); code != "no_session" {
		t.Fatalf("no cookie: %q", code)
	}
	if code := errorOf(t, call(t, h, http.MethodGet, "/api/v1/account", "not-a-session", "", nil), http.StatusUnauthorized); code != "no_session" {
		t.Fatalf("bogus cookie: %q", code)
	}
	aliceCookie := loginAs(t, h, alice)
	bobCookie := loginAs(t, h, bob)
	account := accountOf(t, h, aliceCookie)
	if account.User.Name != "alice" || account.User.Username != "alice" || account.User.Email != "alice@example.test" || account.CSRF == "" || account.User.Groups == nil || account.Methods == nil {
		t.Fatalf("account %+v", account)
	}
	if code := errorOf(t, call(t, h, http.MethodPost, "/api/v1/account/logout", aliceCookie, "", nil), http.StatusForbidden); code != "csrf" {
		t.Fatalf("missing CSRF: %q", code)
	}
	bobCSRF := accountOf(t, h, bobCookie).CSRF
	if code := errorOf(t, call(t, h, http.MethodPost, "/api/v1/account/logout", aliceCookie, bobCSRF, nil), http.StatusForbidden); code != "csrf" {
		t.Fatalf("another session's CSRF: %q", code)
	}
	var stored v1alpha1.User
	if err := h.client.Get(context.Background(), clientKey(bob.Name), &stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Disabled = true
	if err := h.client.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	if code := errorOf(t, call(t, h, http.MethodGet, "/api/v1/account", bobCookie, "", nil), http.StatusUnauthorized); code != "no_session" {
		t.Fatalf("disabled user: %q", code)
	}
}

func TestChangePassword(t *testing.T) {
	h := newHarness(t)
	alice := createUser(t, h, "alice", "")
	cookie := loginAs(t, h, alice)
	csrf := accountOf(t, h, cookie).CSRF
	wrong := call(t, h, http.MethodPost, "/api/v1/account/password", cookie, csrf, map[string]string{"current": "not it", "new": "a much longer passphrase"})
	if code := errorOf(t, wrong, http.StatusForbidden); code != methods.FailureInvalidCredentials {
		t.Fatalf("wrong current password: %q", code)
	}
	short := call(t, h, http.MethodPost, "/api/v1/account/password", cookie, csrf, map[string]string{"current": testPassword, "new": "short"})
	if code := errorOf(t, short, http.StatusBadRequest); code != "weak_password" {
		t.Fatalf("short password: %q", code)
	}
	noContent(t, call(t, h, http.MethodPost, "/api/v1/account/password", cookie, csrf, map[string]string{"current": testPassword, "new": "a much longer passphrase"}))
	fresh, err := h.store.User(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	flow := methods.Flow{ClientIP: "203.0.113.9", Now: time.Now()}
	result, err := h.registry[v1alpha1.MethodPassword].Complete(context.Background(), flow, fresh, methods.Answer{Password: "a much longer passphrase"})
	if err != nil || result.Subject == nil {
		t.Fatalf("the new password must work: %+v, %v", result, err)
	}
	old, err := h.registry[v1alpha1.MethodPassword].Complete(context.Background(), flow, fresh, methods.Answer{Password: testPassword})
	if err != nil || old.Subject != nil {
		t.Fatalf("the old password must stop working: %+v, %v", old, err)
	}
}

func TestChangePasswordRefusedForLDAP(t *testing.T) {
	h := newHarness(t)
	ldapUser := createUser(t, h, "t.farahani", "corp")
	cookie := loginAs(t, h, ldapUser)
	csrf := accountOf(t, h, cookie).CSRF
	resp := call(t, h, http.MethodPost, "/api/v1/account/password", cookie, csrf, map[string]string{"current": testPassword, "new": "a much longer passphrase"})
	if code := errorOf(t, resp, http.StatusConflict); code != "not_local" {
		t.Fatalf("ldap user: %q", code)
	}
}

func TestTOTPEnrollAndVerify(t *testing.T) {
	h := newHarness(t)
	alice := createUser(t, h, "alice", "")
	cookie := loginAs(t, h, alice)
	csrf := accountOf(t, h, cookie).CSRF
	first := decode[methods.TOTPEnrollment](t, call(t, h, http.MethodPost, "/api/v1/account/totp", cookie, csrf, nil), http.StatusOK)
	second := decode[methods.TOTPEnrollment](t, call(t, h, http.MethodPost, "/api/v1/account/totp", cookie, csrf, nil), http.StatusOK)
	if first.Secret == "" || second.Secret == first.Secret || second.OTPAuthURL == "" {
		t.Fatalf("a second enrollment must replace the pending one: %+v %+v", first, second)
	}
	wrong := call(t, h, http.MethodPost, "/api/v1/account/totp/verify", cookie, csrf, map[string]string{"code": "000000"})
	if code := errorOf(t, wrong, http.StatusForbidden); code != methods.FailureInvalidCode {
		t.Fatalf("wrong code: %q", code)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(second.Secret)
	if err != nil {
		t.Fatal(err)
	}
	right := call(t, h, http.MethodPost, "/api/v1/account/totp/verify", cookie, csrf, map[string]string{"code": methods.TOTPCode(seed, methods.TOTPStep(time.Now()))})
	if codes := decode[recoveryAnswer](t, right, http.StatusOK).RecoveryCodes; len(codes) != 10 {
		t.Fatalf("recovery codes %v", codes)
	}
	var enrolled []string
	for _, method := range accountOf(t, h, cookie).Methods {
		if method.EnrolledAt != nil {
			enrolled = append(enrolled, method.Method)
		}
	}
	if !slices.Contains(enrolled, v1alpha1.MethodTOTP) || !slices.Contains(enrolled, v1alpha1.MethodRecovery) {
		t.Fatalf("enrolled methods %v", enrolled)
	}
	if code := errorOf(t, call(t, h, http.MethodPost, "/api/v1/account/totp", cookie, csrf, nil), http.StatusConflict); code != "already_enrolled" {
		t.Fatalf("enroll twice: %q", code)
	}
}

func TestRemoveTOTPRefusedWhenRequired(t *testing.T) {
	h := newHarness(t)
	alice := createUser(t, h, "alice", "")
	cookie := loginAs(t, h, alice)
	csrf := accountOf(t, h, cookie).CSRF
	enrollTOTP(t, h, cookie, csrf)
	ops := &v1alpha1.Group{
		ObjectMeta: metav1.ObjectMeta{Name: "ops", Namespace: release.SystemNamespace},
		Spec:       v1alpha1.GroupSpec{Members: []string{"alice"}, RequireSecondFactor: true},
	}
	create(t, h.client, ops)
	if code := errorOf(t, call(t, h, http.MethodDelete, "/api/v1/account/totp", cookie, csrf, nil), http.StatusConflict); code != "second_factor_required" {
		t.Fatalf("required factor: %q", code)
	}
	if err := h.client.Delete(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	noContent(t, call(t, h, http.MethodDelete, "/api/v1/account/totp", cookie, csrf, nil))
	for _, method := range []string{v1alpha1.MethodTOTP, v1alpha1.MethodRecovery} {
		if exists(t, h.client, &v1alpha1.Credential{}, v1alpha1.CredentialName("alice", method)) {
			t.Fatalf("the %s credential must be gone", method)
		}
	}
}

func TestNewRecoveryCodes(t *testing.T) {
	h := newHarness(t)
	alice := createUser(t, h, "alice", "")
	cookie := loginAs(t, h, alice)
	csrf := accountOf(t, h, cookie).CSRF
	if code := errorOf(t, call(t, h, http.MethodPost, "/api/v1/account/recovery-codes", cookie, csrf, nil), http.StatusConflict); code != "totp_not_enrolled" {
		t.Fatalf("without totp: %q", code)
	}
	first := enrollTOTP(t, h, cookie, csrf)
	second := decode[recoveryAnswer](t, call(t, h, http.MethodPost, "/api/v1/account/recovery-codes", cookie, csrf, nil), http.StatusOK).RecoveryCodes
	if len(second) != 10 || reflect.DeepEqual(first, second) {
		t.Fatalf("new codes %v, first %v", second, first)
	}
}

func TestNewRecoveryCodesCountsAgainstTheRateLimit(t *testing.T) {
	h := newHarnessWithLimiter(t, methods.NewRateLimiter(2, time.Minute))
	alice := createUser(t, h, "alice", "")
	cookie := loginAs(t, h, alice)
	csrf := accountOf(t, h, cookie).CSRF
	enrollTOTP(t, h, cookie, csrf)
	if code := errorOf(t, call(t, h, http.MethodPost, "/api/v1/account/recovery-codes", cookie, csrf, nil), http.StatusForbidden); code != methods.FailureRateLimited {
		t.Fatalf("recovery code generation must count against the per-IP limiter: %q", code)
	}
}

func TestCreateListRevokeToken(t *testing.T) {
	h := newHarness(t)
	alice := createUser(t, h, "alice", "")
	cookie := loginAs(t, h, alice)
	csrf := accountOf(t, h, cookie).CSRF
	past := metav1.NewTime(time.Now().Add(-time.Hour))
	expired := call(t, h, http.MethodPost, "/api/v1/account/tokens", cookie, csrf, map[string]any{"description": "old", "scopes": []string{}, "expiresAt": past})
	if code := errorOf(t, expired, http.StatusBadRequest); code != "invalid_expiry" {
		t.Fatalf("past expiry: %q", code)
	}
	expiry := metav1.NewTime(time.Now().Add(24 * time.Hour).Truncate(time.Second))
	created := decode[map[string]string](t, call(t, h, http.MethodPost, "/api/v1/account/tokens", cookie, csrf, map[string]any{"description": "ci", "scopes": []string{"kube"}, "expiresAt": expiry}), http.StatusCreated)
	token, id := created["token"], created["id"]
	if !secret.IsAPIToken(token) || id != secret.SHA256Hex(token) {
		t.Fatalf("created %v", created)
	}
	var stored v1alpha1.APIToken
	if err := h.client.Get(context.Background(), clientKey(id), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.UserRef != "alice" || stored.Labels[v1alpha1.LabelKind] != "APIToken" || stored.Labels[v1alpha1.LabelName] != id[:63] || !stored.Spec.ExpiresAt.Equal(&expiry) {
		t.Fatalf("stored token %+v", stored)
	}
	listed := decode[[]tokenView](t, call(t, h, http.MethodGet, "/api/v1/account/tokens", cookie, "", nil), http.StatusOK)
	if len(listed) != 1 || listed[0].ID != id || listed[0].Description != "ci" || !reflect.DeepEqual(listed[0].Scopes, []string{"kube"}) || listed[0].LastUsed != nil {
		t.Fatalf("listed %+v", listed)
	}
	noContent(t, call(t, h, http.MethodDelete, "/api/v1/account/tokens/"+id, cookie, csrf, nil))
	if empty := decode[[]tokenView](t, call(t, h, http.MethodGet, "/api/v1/account/tokens", cookie, "", nil), http.StatusOK); empty == nil || len(empty) != 0 {
		t.Fatalf("after revoke %+v", empty)
	}
}

func TestNewToken(t *testing.T) {
	token := "brk_" + "0123456789abcdefghijABCDEFGHIJ0123456789"
	expiry := metav1.NewTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	scopes := []string{"kube"}
	got := NewToken("alice", "ci", scopes, &expiry, token)
	name := secret.SHA256Hex(token)
	if got.Name != name || got.Namespace != release.SystemNamespace || got.Spec.UserRef != "alice" || got.Spec.Description != "ci" {
		t.Fatalf("token %+v", got)
	}
	if got.Labels[v1alpha1.LabelKind] != "APIToken" || got.Labels[v1alpha1.LabelName] != name[:63] {
		t.Fatalf("labels %v", got.Labels)
	}
	scopes[0] = "changed"
	if got.Spec.Scopes[0] != "kube" || got.Spec.ExpiresAt == &expiry || !got.Spec.ExpiresAt.Equal(&expiry) {
		t.Fatalf("NewToken must copy its inputs: %+v", got.Spec)
	}
	if never := NewToken("alice", "", nil, nil, token); never.Spec.ExpiresAt != nil {
		t.Fatalf("no expiry %+v", never.Spec)
	}
}

func TestCannotRevokeOthersSession(t *testing.T) {
	h := newHarness(t)
	alice := createUser(t, h, "alice", "")
	bob := createUser(t, h, "bob", "")
	aliceCookie := loginAs(t, h, alice)
	bobCookie := loginAs(t, h, bob)
	aliceCSRF := accountOf(t, h, aliceCookie).CSRF
	bobCSRF := accountOf(t, h, bobCookie).CSRF
	bobSession := secret.SHA256Hex(bobCookie)
	if code := errorOf(t, call(t, h, http.MethodDelete, "/api/v1/account/sessions/"+bobSession, aliceCookie, aliceCSRF, nil), http.StatusNotFound); code != "not_found" {
		t.Fatalf("revoke another user's session: %q", code)
	}
	if !exists(t, h.client, &v1alpha1.Session{}, bobSession) {
		t.Fatal("bob's session must survive")
	}
	bobToken := decode[map[string]string](t, call(t, h, http.MethodPost, "/api/v1/account/tokens", bobCookie, bobCSRF, map[string]any{"description": "bob"}), http.StatusCreated)["id"]
	if code := errorOf(t, call(t, h, http.MethodDelete, "/api/v1/account/tokens/"+bobToken, aliceCookie, aliceCSRF, nil), http.StatusNotFound); code != "not_found" {
		t.Fatalf("revoke another user's token: %q", code)
	}
	if !exists(t, h.client, &v1alpha1.APIToken{}, bobToken) {
		t.Fatal("bob's token must survive")
	}
	if code := errorOf(t, call(t, h, http.MethodDelete, "/api/v1/account/tokens/not-a-token-id", aliceCookie, aliceCSRF, nil), http.StatusNotFound); code != "not_found" {
		t.Fatalf("malformed id: %q", code)
	}
	sessions := decode[[]sessionView](t, call(t, h, http.MethodGet, "/api/v1/account/sessions", aliceCookie, "", nil), http.StatusOK)
	if len(sessions) != 1 || sessions[0].ID != secret.SHA256Hex(aliceCookie) || !sessions[0].Current || sessions[0].UserAgent != "test-agent" || sessions[0].ClientIP != "203.0.113.1" {
		t.Fatalf("alice's sessions %+v", sessions)
	}
}

func TestExpiredOrMissingSessionClearsCookie(t *testing.T) {
	h := newHarness(t)
	alice := createUser(t, h, "alice", "")
	expiredCookie := "expired-session-cookie-0123456789"
	expired := &v1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{Name: secret.SHA256Hex(expiredCookie), Namespace: release.SystemNamespace},
		Spec: v1alpha1.SessionSpec{
			UserRef:   alice.Name,
			AuthTime:  metav1.Now(),
			ExpiresAt: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
	}
	create(t, h.client, expired)
	for _, given := range []string{expiredCookie, "not-a-session"} {
		resp := call(t, h, http.MethodGet, "/api/v1/account", given, "", nil)
		if code := errorOf(t, resp, http.StatusUnauthorized); code != "no_session" {
			t.Fatalf("cookie %q: %q", given, code)
		}
		var cleared *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == login.CookieSession {
				cleared = c
			}
		}
		if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
			t.Fatalf("cookie %q: the session cookie must be cleared: %+v", given, cleared)
		}
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	h := newHarness(t)
	alice := createUser(t, h, "alice", "")
	first := loginAs(t, h, alice)
	second := loginAs(t, h, alice)
	firstSession := secret.SHA256Hex(first)
	refresh := &v1alpha1.RefreshToken{
		ObjectMeta: metav1.ObjectMeta{Name: secret.SHA256Hex("refresh-of-the-first-session"), Namespace: release.SystemNamespace},
		Spec: v1alpha1.RefreshTokenSpec{
			Family:    "family-1",
			UserRef:   "alice",
			ClientID:  "bedrock-cli",
			AuthTime:  metav1.Now(),
			Session:   firstSession,
			ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)),
		},
	}
	create(t, h.client, refresh)
	csrf := accountOf(t, h, first).CSRF
	resp := call(t, h, http.MethodPost, "/api/v1/account/logout", first, csrf, nil)
	noContent(t, resp)
	var cleared *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == login.CookieSession {
			cleared = cookie
		}
	}
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Fatalf("the session cookie must be cleared: %+v", cleared)
	}
	if exists(t, h.client, &v1alpha1.Session{}, firstSession) || exists(t, h.client, &v1alpha1.RefreshToken{}, refresh.Name) {
		t.Fatal("logout must delete the session and its refresh tokens")
	}
	if code := errorOf(t, call(t, h, http.MethodGet, "/api/v1/account", first, "", nil), http.StatusUnauthorized); code != "no_session" {
		t.Fatalf("after logout: %q", code)
	}
	secondCSRF := accountOf(t, h, second).CSRF
	noContent(t, call(t, h, http.MethodDelete, "/api/v1/account/sessions/"+secret.SHA256Hex(second), second, secondCSRF, nil))
	if code := errorOf(t, call(t, h, http.MethodGet, "/api/v1/account", second, "", nil), http.StatusUnauthorized); code != "no_session" {
		t.Fatalf("after revoking the current session: %q", code)
	}
}

func TestInternalErrorLogsTheCause(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	h := newHarness(t)
	failing := Handler(Deps{
		Store:   h.store,
		Client:  h.client,
		Methods: h.registry,
		Settings: func(context.Context) (policy.Settings, error) {
			return policy.Settings{}, errors.New("settings are unreadable")
		},
		Random:  rand.Reader,
		Clock:   time.Now,
		Limiter: methods.NewRateLimiter(100, time.Minute),
	})
	server := httptest.NewTLSServer(failing)
	t.Cleanup(server.Close)
	broken := harness{client: h.client, store: h.store, registry: h.registry, server: server}
	alice := createUser(t, broken, "alice", "")
	cookie := loginAs(t, broken, alice)
	csrf := accountOf(t, broken, cookie).CSRF
	if code := errorOf(t, call(t, broken, http.MethodDelete, "/api/v1/account/totp", cookie, csrf, nil), http.StatusInternalServerError); code != "internal" {
		t.Fatalf("unreadable settings: %q", code)
	}
	written := logs.String()
	if !strings.Contains(written, "path=/api/v1/account/totp") || !strings.Contains(written, "settings are unreadable") {
		t.Fatalf("the cause is missing from the log: %s", written)
	}
	if strings.Contains(written, cookie) || strings.Contains(written, csrf) {
		t.Fatal("the session cookie or the CSRF token reached the log")
	}
}
