package login

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestStartBindsTheCookie(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	browser := newBrowser(t, h.server)
	id := createAuthRequest(t, h.client)
	resp := send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": id}, nil)
	first := challengeOf(t, resp)
	if first.Type != methods.ChallengeUsername || first.CSRF == "" {
		t.Fatalf("challenge %+v", first)
	}
	cookie := cookieNamed(resp.Cookies(), CookieLogin)
	if cookie == nil || !strings.HasPrefix(cookie.Value, id+".") || len(cookie.Value) != len(id)+1+43 {
		t.Fatalf("login cookie %+v", cookie)
	}
	stored := storedRequest(t, h.client, id)
	if stored.Status.CookieHash != secret.SHA256Hex(cookie.Value) {
		t.Fatalf("cookie hash %q", stored.Status.CookieHash)
	}
	if stored.Status.Login.Step != methods.ChallengeUsername || stored.Status.Login.CSRFHash != secret.SHA256Hex(first.CSRF) {
		t.Fatalf("login state %+v", stored.Status.Login)
	}
	stranger := newBrowser(t, h.server)
	taken := send(t, stranger, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": id}, nil)
	if code := errorOf(t, taken, http.StatusConflict); code != "already_started" {
		t.Fatalf("second browser: %q", code)
	}
	again := challengeOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": id}, nil))
	if again.Type != methods.ChallengeUsername || again.CSRF == "" || again.CSRF == first.CSRF {
		t.Fatalf("restart in the same browser %+v", again)
	}
	unknown := challengeOf(t, send(t, stranger, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": "doesnotexist0000000000"}, nil))
	if unknown.Type != methods.ChallengeErrorType || unknown.Error == nil || unknown.Error.Code != errorExpired {
		t.Fatalf("unknown request %+v", unknown)
	}
	form, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/login/start", strings.NewReader("authRequest="+id))
	if err != nil {
		t.Fatal(err)
	}
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	formResp, err := stranger.Do(form)
	if err != nil {
		t.Fatal(err)
	}
	defer formResp.Body.Close()
	if code := errorOf(t, formResp, http.StatusUnsupportedMediaType); code != "unsupported_media_type" {
		t.Fatalf("form post: %q", code)
	}
}

func TestAnswerNeedsCSRF(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	createLocalUser(t, h.client, "alice")
	browser := newBrowser(t, h.server)
	_, first := startLogin(t, h, browser)
	target := h.server.URL + "/api/v1/login/answer"
	username := methods.Answer{Type: methods.ChallengeUsername, Username: "alice"}
	if code := errorOf(t, send(t, browser, http.MethodPost, target, "", username, nil), http.StatusForbidden); code != "csrf" {
		t.Fatalf("missing header: %q", code)
	}
	if code := errorOf(t, send(t, browser, http.MethodPost, target, "not-the-token", username, nil), http.StatusForbidden); code != "csrf" {
		t.Fatalf("wrong header: %q", code)
	}
	password := answerWith(t, h, browser, first.CSRF, username)
	if password.Type != methods.ChallengePassword || password.CSRF == first.CSRF {
		t.Fatalf("password challenge %+v", password)
	}
	stale := send(t, browser, http.MethodPost, target, first.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword}, nil)
	if code := errorOf(t, stale, http.StatusForbidden); code != "csrf" {
		t.Fatalf("a used CSRF value must not work again: %q", code)
	}
}

func TestAnswerNeedsTheBoundCookie(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	browser := newBrowser(t, h.server)
	id, first := startLogin(t, h, browser)
	target := h.server.URL + "/api/v1/login/answer"
	username := methods.Answer{Type: methods.ChallengeUsername, Username: "alice"}
	stranger := newBrowser(t, h.server)
	if code := errorOf(t, send(t, stranger, http.MethodPost, target, first.CSRF, username, nil), http.StatusUnauthorized); code != "no_login" {
		t.Fatalf("no cookie: %q", code)
	}
	base, err := url.Parse(h.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stranger.Jar.SetCookies(base, []*http.Cookie{{Name: CookieLogin, Value: id + ".forgedforgedforgedforgedforgedforgedforgedf", Path: "/", Secure: true}})
	if code := errorOf(t, send(t, stranger, http.MethodPost, target, first.CSRF, username, nil), http.StatusUnauthorized); code != "no_login" {
		t.Fatalf("forged cookie: %q", code)
	}
	if code := errorOf(t, get(t, stranger, h.server.URL+"/api/v1/login/challenge"), http.StatusUnauthorized); code != "no_login" {
		t.Fatalf("challenge with a forged cookie: %q", code)
	}
	current := challengeOf(t, get(t, browser, h.server.URL+"/api/v1/login/challenge"))
	if current.Type != methods.ChallengeUsername || current.CSRF == "" {
		t.Fatalf("challenge %+v", current)
	}
	if code := errorOf(t, send(t, browser, http.MethodPost, target, current.CSRF, methods.Answer{Type: methods.ChallengeTOTP, Code: "123456"}, nil), http.StatusBadRequest); code != "unexpected_answer" {
		t.Fatalf("an answer for another step: %q", code)
	}
}

func TestPasswordLoginIssuesDone(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	createLocalUser(t, h.client, "alice")
	browser := newBrowser(t, h.server)
	id, first := startLogin(t, h, browser)
	password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "  Alice\t"})
	if password.Type != methods.ChallengePassword || password.Username != "alice" {
		t.Fatalf("the username must be trimmed and lowercased: %+v", password)
	}
	wrong := answerWith(t, h, browser, password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: "not it"})
	if wrong.Type != methods.ChallengePassword || wrong.Error == nil || wrong.Error.Code != methods.FailureInvalidCredentials {
		t.Fatalf("wrong password %+v", wrong)
	}
	resp := send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/answer", wrong.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword}, nil)
	done := challengeOf(t, resp)
	if done.Type != methods.ChallengeDone || done.Redirect != "/oauth/v2/authorize/callback?id="+id {
		t.Fatalf("done %+v", done)
	}
	session := cookieNamed(resp.Cookies(), CookieSession)
	if session == nil || session.Value == "" || !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode || session.MaxAge != int((12*time.Hour).Seconds()) {
		t.Fatalf("session cookie %+v", session)
	}
	if cleared := cookieNamed(resp.Cookies(), CookieLogin); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("the login cookie must be cleared: %+v", cleared)
	}
	stored := storedRequest(t, h.client, id)
	if !stored.Status.Done || stored.Status.Subject != "alice" || !slices.Contains(stored.Status.AMR, "pwd") || stored.Status.AuthTime == nil {
		t.Fatalf("status %+v", stored.Status)
	}
	if stored.Status.Session != secret.SHA256Hex(session.Value) {
		t.Fatalf("status session %q", stored.Status.Session)
	}
	found, err := h.store.SessionByCookie(context.Background(), session.Value)
	if err != nil || found.Spec.UserRef != "alice" {
		t.Fatalf("session %+v, %v", found.Spec, err)
	}
}

func TestUnicodeLookalikeUsernameFails(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	createLocalUser(t, h.client, "alice")
	browser := newBrowser(t, h.server)
	_, first := startLogin(t, h, browser)
	lookalike := "аlice"
	password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: lookalike})
	if password.Type != methods.ChallengePassword || password.Username != lookalike {
		t.Fatalf("a refused username must still look like a normal one: %+v", password)
	}
	failed := answerWith(t, h, browser, password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword})
	if failed.Error == nil || failed.Error.Code != methods.FailureInvalidCredentials {
		t.Fatalf("a look-alike must not reach alice: %+v", failed)
	}
}

func TestEnrollInsideLoginOverHTTP(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	createLocalUser(t, h.client, "alice")
	create(t, h.client, &v1alpha1.Group{
		ObjectMeta: metav1.ObjectMeta{Name: "ops", Namespace: release.SystemNamespace},
		Spec:       v1alpha1.GroupSpec{Members: []string{"alice"}, RequireSecondFactor: true},
	})
	browser := newBrowser(t, h.server)
	_, first := startLogin(t, h, browser)
	password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"})
	enroll := answerWith(t, h, browser, password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword})
	if enroll.Type != methods.ChallengeTOTPEnroll || enroll.Enroll == nil || enroll.Enroll.Secret == "" {
		t.Fatalf("enroll %+v", enroll)
	}
	reloaded := challengeOf(t, get(t, browser, h.server.URL+"/api/v1/login/challenge"))
	if reloaded.Type != methods.ChallengeTOTPEnroll || reloaded.Enroll == nil || reloaded.Enroll.Secret == enroll.Enroll.Secret {
		t.Fatalf("a reload must start a new enrollment: %+v", reloaded)
	}
	wrong := answerWith(t, h, browser, reloaded.CSRF, methods.Answer{Type: methods.ChallengeTOTPEnroll, Code: "000000"})
	if wrong.Type != methods.ChallengeTOTPEnroll || wrong.Error == nil || wrong.Error.Code != methods.FailureInvalidCode || wrong.Enroll != nil {
		t.Fatalf("wrong code %+v", wrong)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(reloaded.Enroll.Secret)
	if err != nil {
		t.Fatal(err)
	}
	codes := answerWith(t, h, browser, wrong.CSRF, methods.Answer{Type: methods.ChallengeTOTPEnroll, Code: methods.TOTPCode(seed, methods.TOTPStep(time.Now()))})
	if codes.Type != methods.ChallengeTOTPEnroll || len(codes.RecoveryCodes) != 10 || codes.Enroll != nil {
		t.Fatalf("recovery codes %+v", codes)
	}
	done := answerWith(t, h, browser, codes.CSRF, methods.Answer{Type: methods.ChallengeTOTPEnroll})
	if done.Type != methods.ChallengeDone || done.Redirect == "" {
		t.Fatalf("done %+v", done)
	}
}

func TestDeviceLoginApproves(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	createLocalUser(t, h.client, "alice")
	ctx := context.Background()
	expires := time.Now().Add(10 * time.Minute)
	scopes := []string{"openid", "offline_access"}
	if err := h.store.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-approve", "BCDFGHJK", expires, scopes); err != nil {
		t.Fatal(err)
	}
	if err := h.store.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-deny", "LMNPQRST", expires, scopes); err != nil {
		t.Fatal(err)
	}
	stranger := newBrowser(t, h.server)
	unknown := send(t, stranger, http.MethodPost, h.server.URL+"/api/v1/login/device", "", map[string]string{"userCode": "ZZZZ-ZZZZ"}, nil)
	if code := errorOf(t, unknown, http.StatusNotFound); code != methods.FailureInvalidCode {
		t.Fatalf("unknown user code: %q", code)
	}
	confirm := deviceLoginUntilConfirm(t, h, " bcdf-ghjk ")
	want := &methods.DeviceChallenge{UserCode: "BCDF-GHJK", ClientID: "bedrock-cli", Scopes: scopes}
	if confirm.challenge.Type != methods.ChallengeDeviceConfirm || !reflect.DeepEqual(confirm.challenge.Device, want) {
		t.Fatalf("confirm %+v", confirm.challenge)
	}
	approve := true
	done := answerWith(t, h, confirm.browser, confirm.challenge.CSRF, methods.Answer{Type: methods.ChallengeDeviceConfirm, Approve: &approve})
	if done.Type != methods.ChallengeDone || done.Redirect != "" {
		t.Fatalf("done %+v", done)
	}
	state, err := h.store.GetDeviceAuthorizatonState(ctx, "bedrock-cli", "device-code-approve")
	if err != nil || !state.Done || state.Subject != "alice" {
		t.Fatalf("device state %+v, %v", state, err)
	}
	denying := deviceLoginUntilConfirm(t, h, "LMNP-QRST")
	deny := false
	answerWith(t, h, denying.browser, denying.challenge.CSRF, methods.Answer{Type: methods.ChallengeDeviceConfirm, Approve: &deny})
	denied, err := h.store.GetDeviceAuthorizatonState(ctx, "bedrock-cli", "device-code-deny")
	if err != nil || !denied.Denied || denied.Done {
		t.Fatalf("denied state %+v, %v", denied, err)
	}
}

type deviceLogin struct {
	browser   *http.Client
	challenge methods.Challenge
}

func deviceLoginUntilConfirm(t *testing.T, h harness, userCode string) deviceLogin {
	t.Helper()
	browser := newBrowser(t, h.server)
	first := challengeOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/device", "", map[string]string{"userCode": userCode}, nil))
	if first.Type != methods.ChallengeUsername {
		t.Fatalf("device start %+v", first)
	}
	password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"})
	return deviceLogin{browser: browser, challenge: answerWith(t, h, browser, password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword})}
}

func TestUpstreamCallbackChecksState(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	carol := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "carol", Namespace: release.SystemNamespace},
		Spec:       v1alpha1.UserSpec{Username: "carol", Source: "dex", Methods: []string{v1alpha1.MethodOIDC}},
	}
	create(t, h.client, carol, &v1alpha1.IdentityProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "dex", Namespace: release.SystemNamespace},
		Spec: v1alpha1.IdentityProviderSpec{
			Type:        v1alpha1.MethodOIDC,
			DisplayName: "Dadehat SSO",
			OIDC:        &v1alpha1.OIDCProvider{Issuer: "https://dex.example.test", ClientID: "bedrock"},
			SecretRef:   "dex-client",
		},
	})
	h.upstream.respondWith(methods.Subject{User: *carol, AMR: []string{"fed"}})
	browser := newBrowser(t, h.server)
	id, first := startLogin(t, h, browser)
	if !reflect.DeepEqual(first.Providers, []methods.ProviderChoice{{Name: "dex", DisplayName: "Dadehat SSO", Type: v1alpha1.MethodOIDC}}) {
		t.Fatalf("providers %+v", first.Providers)
	}
	resp := send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/answer", first.CSRF, methods.Answer{Type: answerProvider, Provider: "dex"}, nil)
	redirect := challengeOf(t, resp)
	if redirect.Type != methods.ChallengeRedirect || redirect.Redirect != "https://dex.example.test/authorize" {
		t.Fatalf("redirect %+v", redirect)
	}
	upstream := cookieNamed(resp.Cookies(), CookieUpstream)
	if upstream == nil || !upstream.HttpOnly || !upstream.Secure || upstream.MaxAge != 600 {
		t.Fatalf("upstream cookie %+v", upstream)
	}
	begins, _ := h.upstream.calls()
	if len(begins) != 1 || begins[0].AuthRequest.Status.Login.Upstream != upstream.Value {
		t.Fatalf("Begin must receive the cookie value: %+v", begins)
	}
	decoded, err := methods.DecodeUpstream(upstream.Value)
	if err != nil || decoded.State != id || len(decoded.Verifier) != 64 || len(decoded.Nonce) != 32 {
		t.Fatalf("upstream cookie %+v, %v", decoded, err)
	}
	if stored := storedRequest(t, h.client, id); stored.Status.Login.Upstream != secret.SHA256Hex(upstream.Value) {
		t.Fatalf("etcd must keep only the hash: %q", stored.Status.Login.Upstream)
	}
	base, err := url.Parse(h.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	bare := newBrowser(t, h.server)
	bare.Jar.SetCookies(base, []*http.Cookie{{Name: CookieLogin, Value: cookieNamed(browser.Jar.Cookies(base), CookieLogin).Value, Path: "/", Secure: true}})
	if code := errorOf(t, get(t, bare, callbackURL(h, "dex", decoded.State)), http.StatusBadRequest); code != "invalid_state" {
		t.Fatalf("no upstream cookie: %q", code)
	}
	if code := errorOf(t, get(t, browser, callbackURL(h, "dex", "wrong")), http.StatusBadRequest); code != "invalid_state" {
		t.Fatalf("wrong state: %q", code)
	}
	if code := errorOf(t, get(t, browser, callbackURL(h, "other", decoded.State)), http.StatusBadRequest); code != "invalid_state" {
		t.Fatalf("other provider: %q", code)
	}
	if _, completes := h.upstream.calls(); len(completes) != 0 {
		t.Fatalf("a refused callback must not reach the method: %+v", completes)
	}
	good := get(t, browser, callbackURL(h, "dex", decoded.State))
	if good.StatusCode != http.StatusSeeOther || good.Header.Get("Location") != "/oauth/v2/authorize/callback?id="+id {
		t.Fatalf("callback %d %q", good.StatusCode, good.Header.Get("Location"))
	}
	_, completes := h.upstream.calls()
	if len(completes) != 1 || completes[0].answer.Code != "abc" || completes[0].answer.Provider != "dex" || completes[0].flow.AuthRequest.Status.Login.Upstream != upstream.Value {
		t.Fatalf("Complete %+v", completes)
	}
	if replay := get(t, browser, callbackURL(h, "dex", decoded.State)); replay.StatusCode == http.StatusSeeOther {
		t.Fatal("a callback must not work twice")
	}
}

func callbackURL(h harness, provider, state string) string {
	return h.server.URL + "/api/v1/login/providers/" + url.PathEscape(provider) + "/callback?code=abc&state=" + url.QueryEscape(state)
}

func TestRateLimitPerClientIP(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(2, time.Minute))
	createLocalUser(t, h.client, "alice")
	browser := newBrowser(t, h.server)
	_, first := startLogin(t, h, browser)
	password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"})
	from := func(ip string) http.Header { return http.Header{"X-Forwarded-For": []string{"10.0.0.1, " + ip}} }
	target := h.server.URL + "/api/v1/login/answer"
	wrong := methods.Answer{Type: methods.ChallengePassword, Password: "wrong"}
	csrf := password.CSRF
	for i, want := range []string{methods.FailureInvalidCredentials, methods.FailureInvalidCredentials, methods.FailureRateLimited} {
		challenge := challengeOf(t, send(t, browser, http.MethodPost, target, csrf, wrong, from("203.0.113.7")))
		if challenge.Type != methods.ChallengePassword || challenge.Error == nil || challenge.Error.Code != want {
			t.Fatalf("attempt %d: %+v, want %s", i+1, challenge, want)
		}
		csrf = challenge.CSRF
	}
	other := challengeOf(t, send(t, browser, http.MethodPost, target, csrf, wrong, from("198.51.100.9")))
	if other.Error == nil || other.Error.Code != methods.FailureInvalidCredentials {
		t.Fatalf("another client IP has its own budget: %+v", other)
	}
}

func TestFiveWrongSecondFactorAnswersEndTheLogin(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	user := createLocalUser(t, h.client, "alice")
	ctx := context.Background()
	totp := methods.NewTOTP(h.client, rand.Reader, func(context.Context) (string, error) { return "https://sso.example.test", nil })
	enrollment, err := totp.Enroll(ctx, *user, methods.Answer{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.TOTP.Secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if result, err := totp.Complete(ctx, methods.Flow{Now: now}, *user, methods.Answer{Code: methods.TOTPCode(seed, methods.TOTPStep(now))}); err != nil || result.Subject == nil {
		t.Fatalf("confirm the enrollment: %+v %v", result, err)
	}
	step := methods.TOTPStep(now)
	valid := []string{methods.TOTPCode(seed, step-1), methods.TOTPCode(seed, step), methods.TOTPCode(seed, step+1), methods.TOTPCode(seed, step+2)}
	guess := "000000"
	if slices.Contains(valid, guess) {
		guess = "111111"
	}
	browser := newBrowser(t, h.server)
	_, first := startLogin(t, h, browser)
	password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"})
	challenge := answerWith(t, h, browser, password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword})
	if challenge.Type != methods.ChallengeTOTP {
		t.Fatalf("second factor %+v", challenge)
	}
	answers := []methods.Answer{
		{Type: methods.ChallengeTOTP, Code: guess},
		{Type: methods.ChallengeTOTP, Code: guess},
		{Type: methods.ChallengeRecovery},
		{Type: methods.ChallengeRecovery, Code: "aaaaa-bbbbb"},
		{Type: methods.ChallengeRecovery, Code: "ccccc-ddddd"},
	}
	for index, given := range answers {
		challenge = answerWith(t, h, browser, challenge.CSRF, given)
		if given.Code != "" && (challenge.Error == nil || challenge.Error.Code != methods.FailureInvalidCode) {
			t.Fatalf("answer %d: %+v", index+1, challenge)
		}
	}
	ended := answerWith(t, h, browser, challenge.CSRF, methods.Answer{Type: methods.ChallengeRecovery, Code: "eeeee-fffff"})
	if ended.Type != methods.ChallengeErrorType || ended.Error == nil || ended.Error.Code != errorExpired {
		t.Fatalf("the fifth wrong answer must end the login: %+v", ended)
	}
	right := methods.Answer{Type: methods.ChallengeTOTP, Code: methods.TOTPCode(seed, methods.TOTPStep(time.Now()))}
	if code := errorOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/answer", ended.CSRF, right, nil), http.StatusBadRequest); code != "unexpected_answer" {
		t.Fatalf("a right code after the end: %q", code)
	}
	if reloaded := challengeOf(t, get(t, browser, h.server.URL+"/api/v1/login/challenge")); reloaded.Type != methods.ChallengeErrorType {
		t.Fatalf("a reload must keep the end: %+v", reloaded)
	}
}

func TestUsernameLookupsAreRateLimitedPerClientIP(t *testing.T) {
	h := newHarnessWithLookups(t, methods.NewRateLimiter(100, time.Minute), methods.NewRateLimiter(2, time.Minute))
	createLocalUser(t, h.client, "alice")
	create(t, h.client,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "corp-bind", Namespace: release.SystemNamespace}, Data: map[string][]byte{"bindPassword": []byte("bind")}},
		&v1alpha1.IdentityProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "corp", Namespace: release.SystemNamespace},
			Spec: v1alpha1.IdentityProviderSpec{
				Type:      v1alpha1.MethodLDAP,
				SecretRef: "corp-bind",
				LDAP: &v1alpha1.LDAPProvider{
					URL:        "ldaps://ldap.example.test",
					BindDN:     "cn=reader",
					UserSearch: v1alpha1.LDAPUserSearch{BaseDN: "ou=people", UsernameAttribute: "uid"},
				},
			},
		},
	)
	from := func(ip string) http.Header { return http.Header{"X-Forwarded-For": []string{ip}} }
	lookup := func(username, ip string) methods.Challenge {
		t.Helper()
		browser := newBrowser(t, h.server)
		_, first := startLogin(t, h, browser)
		return challengeOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/answer", first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: username}, from(ip)))
	}
	for _, username := range []string{"ghost", "alice"} {
		if step := lookup(username, "203.0.113.7"); step.Type != methods.ChallengePassword || step.Error != nil {
			t.Fatalf("%s: %+v", username, step)
		}
	}
	limited := lookup("phantom", "203.0.113.7")
	if limited.Type != methods.ChallengeUsername || limited.Error == nil || limited.Error.Code != methods.FailureRateLimited || limited.CSRF == "" {
		t.Fatalf("the third lookup from one address: %+v", limited)
	}
	if dials := h.ldapDials.Load(); dials != 1 {
		t.Fatalf("LDAP dials %d, want 1: a limited lookup must not reach the directory", dials)
	}
	if step := lookup("phantom", "198.51.100.9"); step.Type != methods.ChallengePassword || step.Error != nil {
		t.Fatalf("another address has its own budget: %+v", step)
	}
	if dials := h.ldapDials.Load(); dials != 2 {
		t.Fatalf("LDAP dials %d, want 2", dials)
	}
}

func TestUnknownUserAnswersLikeAWrongPassword(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	createLocalUser(t, h.client, "alice")
	attempt := func(username string) methods.Challenge {
		browser := newBrowser(t, h.server)
		_, first := startLogin(t, h, browser)
		password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: username})
		if password.Type != methods.ChallengePassword || password.Username != username || password.Error != nil {
			t.Fatalf("password challenge for %q: %+v", username, password)
		}
		failed := answerWith(t, h, browser, password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: "not it"})
		if failed.CSRF == "" || failed.Username != username {
			t.Fatalf("failure for %q: %+v", username, failed)
		}
		return withCSRF(withName(failed, ""), "")
	}
	known := attempt("alice")
	unknown := attempt("nobody")
	if !reflect.DeepEqual(known, unknown) || known.Error == nil || known.Error.Code != methods.FailureInvalidCredentials {
		t.Fatalf("known %+v, unknown %+v", known, unknown)
	}
}

func withName(challenge methods.Challenge, username string) methods.Challenge {
	next := challenge
	next.Username = username
	return next
}

func TestDoneLoginRefusesChanges(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	createLocalUser(t, h.client, "alice")
	browser := newBrowser(t, h.server)
	id := createAuthRequest(t, h.client)
	started := send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": id}, nil)
	first := challengeOf(t, started)
	loginCookie := cookieNamed(started.Cookies(), CookieLogin)
	password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"})
	done := answerWith(t, h, browser, password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword})
	if done.Type != methods.ChallengeDone || done.CSRF == "" {
		t.Fatalf("done %+v", done)
	}
	before := storedRequest(t, h.client, id)
	base, err := url.Parse(h.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	replay := newBrowser(t, h.server)
	replay.Jar.SetCookies(base, []*http.Cookie{{Name: CookieLogin, Value: loginCookie.Value, Path: "/", Secure: true}})
	responses := map[string]*http.Response{
		"answer":    send(t, replay, http.MethodPost, h.server.URL+"/api/v1/login/answer", done.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"}, nil),
		"challenge": get(t, replay, h.server.URL+"/api/v1/login/challenge"),
		"start":     send(t, replay, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": id}, nil),
		"callback":  get(t, replay, callbackURL(h, "dex", id)),
	}
	for name, resp := range responses {
		refused := challengeOf(t, resp)
		if refused.Type != methods.ChallengeErrorType || refused.Error == nil || refused.Error.Code != errorExpired || refused.CSRF != "" {
			t.Fatalf("%s on a done login: %+v", name, refused)
		}
	}
	if after := storedRequest(t, h.client, id); after.ResourceVersion != before.ResourceVersion {
		t.Fatalf("a done login must not change: %+v", after.Status)
	}
}

func TestAnswerRefusesALoginDoneMeanwhile(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	createLocalUser(t, h.client, "alice")
	browser := newBrowser(t, h.server)
	id, first := startLogin(t, h, browser)
	h.reader.afterNextGet(isAuthRequest, func() {
		advance(t, h, id, func(status v1alpha1.AuthRequestStatus) v1alpha1.AuthRequestStatus {
			status.Done = true
			return status
		})
	})
	refused := challengeOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/answer", first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"}, nil))
	if refused.Type != methods.ChallengeErrorType || refused.Error == nil || refused.Error.Code != errorExpired {
		t.Fatalf("answer on a login done meanwhile: %+v", refused)
	}
	stored := storedRequest(t, h.client, id)
	if !stored.Status.Done || stored.Status.Login.Step != methods.ChallengeUsername || stored.Status.Login.CSRFHash != secret.SHA256Hex(first.CSRF) {
		t.Fatalf("the done login must stay as it was: %+v", stored.Status)
	}
}

func TestAnswerRefusesAConcurrentCSRFUse(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	createLocalUser(t, h.client, "alice")
	browser := newBrowser(t, h.server)
	id, first := startLogin(t, h, browser)
	password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"})
	winner := secret.SHA256Hex("the answer that came first")
	h.reader.afterNextGet(isAuthRequest, func() {
		advance(t, h, id, func(status v1alpha1.AuthRequestStatus) v1alpha1.AuthRequestStatus {
			status.Login.CSRFHash = winner
			return status
		})
	})
	resp := send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/answer", password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword}, nil)
	if code := errorOf(t, resp, http.StatusForbidden); code != "csrf" {
		t.Fatalf("a CSRF value used meanwhile: %q", code)
	}
	if session := cookieNamed(resp.Cookies(), CookieSession); session != nil {
		t.Fatalf("a refused answer must not set a session: %+v", session)
	}
	if login := cookieNamed(resp.Cookies(), CookieLogin); login != nil {
		t.Fatalf("a refused answer must not touch the login cookie: %+v", login)
	}
	stored := storedRequest(t, h.client, id)
	if stored.Status.Done || stored.Status.Session != "" || stored.Status.Login.Step != methods.ChallengePassword || stored.Status.Login.CSRFHash != winner {
		t.Fatalf("the first answer must win: %+v", stored.Status)
	}
}

func advance(t *testing.T, h harness, id string, change func(v1alpha1.AuthRequestStatus) v1alpha1.AuthRequestStatus) {
	t.Helper()
	var request v1alpha1.AuthRequest
	if err := h.client.Get(context.Background(), client.ObjectKey{Namespace: release.SystemNamespace, Name: id}, &request); err != nil {
		t.Errorf("read %s: %v", id, err)
		return
	}
	request.Status = change(*request.Status.DeepCopy())
	if err := h.client.Status().Update(context.Background(), &request); err != nil {
		t.Errorf("advance %s: %v", id, err)
	}
}

func TestDeviceEntryIsRateLimited(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(2, time.Minute))
	browser := newBrowser(t, h.server)
	from := func(ip string) http.Header { return http.Header{"X-Forwarded-For": []string{ip}} }
	target := h.server.URL + "/api/v1/login/device"
	body := map[string]string{"userCode": "ZZZZ-ZZZZ"}
	for i, want := range []int{http.StatusNotFound, http.StatusNotFound, http.StatusTooManyRequests} {
		resp := send(t, browser, http.MethodPost, target, "", body, from("203.0.113.7"))
		if resp.StatusCode != want {
			t.Fatalf("attempt %d: status %d, want %d", i+1, resp.StatusCode, want)
		}
	}
	if code := errorOf(t, send(t, browser, http.MethodPost, target, "", body, from("203.0.113.7")), http.StatusTooManyRequests); code != methods.FailureRateLimited {
		t.Fatalf("limited code %q", code)
	}
	if code := errorOf(t, send(t, browser, http.MethodPost, target, "", body, from("198.51.100.9")), http.StatusNotFound); code != methods.FailureInvalidCode {
		t.Fatalf("another client IP has its own budget: %q", code)
	}
}

func TestChallengeKeepsATOTPEnrolledMeanwhile(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	user := createLocalUser(t, h.client, "alice")
	create(t, h.client, &v1alpha1.Group{
		ObjectMeta: metav1.ObjectMeta{Name: "ops", Namespace: release.SystemNamespace},
		Spec:       v1alpha1.GroupSpec{Members: []string{"alice"}, RequireSecondFactor: true},
	})
	browser := newBrowser(t, h.server)
	_, first := startLogin(t, h, browser)
	password := answerWith(t, h, browser, first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"})
	enroll := answerWith(t, h, browser, password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword})
	if enroll.Type != methods.ChallengeTOTPEnroll || enroll.Enroll == nil {
		t.Fatalf("enroll %+v", enroll)
	}
	key := client.ObjectKey{Namespace: release.SystemNamespace, Name: v1alpha1.CredentialName(user.Name, v1alpha1.MethodTOTP)}
	var pending v1alpha1.Credential
	if err := h.client.Get(context.Background(), key, &pending); err != nil {
		t.Fatal(err)
	}
	h.handler.afterNextGet(isCredential, func() { markEnrolled(t, h, key) })
	reloaded := challengeOf(t, get(t, browser, h.server.URL+"/api/v1/login/challenge"))
	if reloaded.Type != methods.ChallengeTOTPEnroll || reloaded.Enroll != nil || reloaded.Error != nil || reloaded.CSRF == "" {
		t.Fatalf("a reload must not replace a credential enrolled meanwhile: %+v", reloaded)
	}
	var kept v1alpha1.Credential
	if err := h.client.Get(context.Background(), key, &kept); err != nil || kept.UID != pending.UID || kept.Status.EnrolledAt == nil {
		t.Fatalf("the enrolled credential must stay: %+v, %v", kept.Status, err)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enroll.Enroll.Secret)
	if err != nil {
		t.Fatal(err)
	}
	codes := answerWith(t, h, browser, reloaded.CSRF, methods.Answer{Type: methods.ChallengeTOTPEnroll, Code: methods.TOTPCode(seed, methods.TOTPStep(time.Now()))})
	if codes.Type != methods.ChallengeTOTPEnroll || len(codes.RecoveryCodes) != 10 {
		t.Fatalf("the first secret must still work: %+v", codes)
	}
}

func markEnrolled(t *testing.T, h harness, key client.ObjectKey) {
	t.Helper()
	var credential v1alpha1.Credential
	if err := h.client.Get(context.Background(), key, &credential); err != nil {
		t.Errorf("read %s: %v", key.Name, err)
		return
	}
	credential.Status.EnrolledAt = &metav1.Time{Time: time.Now()}
	if err := h.client.Status().Update(context.Background(), &credential); err != nil {
		t.Errorf("enroll %s: %v", key.Name, err)
	}
}

func TestUpstreamCallbackRefusesADenial(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	create(t, h.client, &v1alpha1.IdentityProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "dex", Namespace: release.SystemNamespace},
		Spec: v1alpha1.IdentityProviderSpec{
			Type:        v1alpha1.MethodOIDC,
			DisplayName: "Dadehat SSO",
			OIDC:        &v1alpha1.OIDCProvider{Issuer: "https://dex.example.test", ClientID: "bedrock"},
			SecretRef:   "dex-client",
		},
	})
	browser := newBrowser(t, h.server)
	id, first := startLogin(t, h, browser)
	csrf := first.CSRF
	for _, query := range []string{"error=access_denied&error_description=denied", "", "code=", "code=abc&error=server_error"} {
		redirect := answerWith(t, h, browser, csrf, methods.Answer{Type: answerProvider, Provider: "dex"})
		if redirect.Type != methods.ChallengeRedirect {
			t.Fatalf("redirect %+v", redirect)
		}
		resp := get(t, browser, h.server.URL+"/api/v1/login/providers/dex/callback?"+query+"&state="+url.QueryEscape(id))
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login/?authRequest="+id {
			t.Fatalf("callback %q: %d %q", query, resp.StatusCode, resp.Header.Get("Location"))
		}
		if cleared := cookieNamed(resp.Cookies(), CookieUpstream); cleared == nil || cleared.MaxAge >= 0 {
			t.Fatalf("callback %q must clear the upstream cookie: %+v", query, cleared)
		}
		if stored := storedRequest(t, h.client, id); stored.Status.Login.Step != methods.ChallengeProviders || stored.Status.Login.Error != methods.FailureProviderError || stored.Status.Done {
			t.Fatalf("callback %q: %+v", query, stored.Status)
		}
		current := challengeOf(t, get(t, browser, h.server.URL+"/api/v1/login/challenge"))
		if current.Type != methods.ChallengeProviders || current.Error == nil || current.Error.Code != methods.FailureProviderError {
			t.Fatalf("challenge after %q: %+v", query, current)
		}
		csrf = current.CSRF
	}
	if _, completes := h.upstream.calls(); len(completes) != 0 {
		t.Fatalf("a denial must not reach the method: %+v", completes)
	}
}

func TestLoginRefusesALargeBody(t *testing.T) {
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	browser := newBrowser(t, h.server)
	large := map[string]string{"authRequest": strings.Repeat("a", 70<<10)}
	if code := errorOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/start", "", large, nil), http.StatusBadRequest); code != "invalid_request" {
		t.Fatalf("large start body: %q", code)
	}
	if code := errorOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/device", "", map[string]string{"userCode": large["authRequest"]}, nil), http.StatusBadRequest); code != "invalid_request" {
		t.Fatalf("large device body: %q", code)
	}
}

func TestInternalErrorLogsTheCause(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	h := newHarness(t, methods.NewRateLimiter(100, time.Minute))
	browser := newBrowser(t, h.server)
	_, first := startLogin(t, h, browser)
	if err := h.client.Delete(context.Background(), &v1alpha1.OAuthClient{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-cli", Namespace: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	resp := send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/answer", first.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "alice"}, nil)
	if code := errorOf(t, resp, http.StatusInternalServerError); code != "internal" {
		t.Fatalf("missing client: %q", code)
	}
	written := logs.String()
	if !strings.Contains(written, `path=/api/v1/login/answer`) || !strings.Contains(written, "not found") {
		t.Fatalf("the cause is missing from the log: %s", written)
	}
	loginURL, err := url.Parse(h.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range browser.Jar.Cookies(loginURL) {
		if strings.Contains(written, cookie.Value) {
			t.Fatalf("the cookie %s reached the log", cookie.Name)
		}
	}
	if strings.Contains(written, first.CSRF) {
		t.Fatal("the CSRF token reached the log")
	}
}
