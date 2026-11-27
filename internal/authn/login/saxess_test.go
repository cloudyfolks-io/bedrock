package login

import (
	"context"
	"encoding/base32"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/saxess"
)

const testConsoleRedirect = "https://console.example.test/oauth/callback"

func consoleClientSpec() v1alpha1.OAuthClientSpec {
	return v1alpha1.OAuthClientSpec{
		ClientID: "bedrock-console", Public: true,
		RedirectURIs: []string{testConsoleRedirect, "https://console.example.test/other"},
		GrantTypes:   []string{v1alpha1.GrantAuthorizationCode},
	}
}

func consoleRequestSpec() v1alpha1.AuthRequestSpec {
	return v1alpha1.AuthRequestSpec{
		ClientID: "bedrock-console", RedirectURI: testConsoleRedirect,
		ResponseType: "code", CodeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", CodeChallengeMethod: "S256",
	}
}

func TestSAxessRequiresExactConsoleAuthorization(t *testing.T) {
	cfg := SAxessConfig{ClientID: "bedrock-console", RedirectURI: testConsoleRedirect}
	for _, tc := range []struct {
		name   string
		change func(*v1alpha1.AuthRequestSpec, *v1alpha1.OAuthClientSpec)
	}{
		{"console", nil},
		{"cli", func(r *v1alpha1.AuthRequestSpec, _ *v1alpha1.OAuthClientSpec) { r.ClientID = "bedrock-cli" }},
		{"other registered redirect", func(r *v1alpha1.AuthRequestSpec, c *v1alpha1.OAuthClientSpec) { r.RedirectURI = c.RedirectURIs[1] }},
		{"unregistered redirect", func(_ *v1alpha1.AuthRequestSpec, c *v1alpha1.OAuthClientSpec) { c.RedirectURIs = nil }},
		{"device", func(r *v1alpha1.AuthRequestSpec, _ *v1alpha1.OAuthClientSpec) { r.DeviceRequest = "device" }},
		{"no code", func(r *v1alpha1.AuthRequestSpec, _ *v1alpha1.OAuthClientSpec) { r.ResponseType = "token" }},
		{"plain", func(r *v1alpha1.AuthRequestSpec, _ *v1alpha1.OAuthClientSpec) { r.CodeChallengeMethod = "plain" }},
		{"invalid PKCE", func(r *v1alpha1.AuthRequestSpec, _ *v1alpha1.OAuthClientSpec) { r.CodeChallenge = "abc" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, oauth := consoleRequestSpec(), consoleClientSpec()
			if tc.change != nil {
				tc.change(&req, &oauth)
			}
			if got := cfg.eligible(req, oauth); got != (tc.change == nil) {
				t.Fatalf("eligible=%t", got)
			}
		})
	}
}

type fakeSAxess struct {
	mu        sync.Mutex
	challenge saxess.Challenge
	onRedeem  func()
}

func (b *fakeSAxess) CreateChallenge(_ context.Context, op, tx string, ttl time.Duration) (saxess.Challenge, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.challenge.OperatorID == op && b.challenge.BrowserTransactionID == tx && time.Now().Before(b.challenge.ExpiresAt) {
		return b.challenge, nil
	}
	var err error
	b.challenge, err = saxess.NewChallenge(op, tx, time.Now(), ttl)
	return b.challenge, err
}

func (b *fakeSAxess) RedeemBrowserApproval(_ context.Context, op, tx, nonce string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.challenge.Nonce != nonce || b.challenge.OperatorID != op || b.challenge.BrowserTransactionID != tx ||
		!time.Now().Before(b.challenge.ExpiresAt) {
		return "", saxess.ErrInvalidChallenge
	}
	if b.challenge.State == saxess.ChallengeConsumed {
		return op, nil
	}
	next, err := b.challenge.Consume(op, tx, time.Now())
	if err != nil {
		return "", err
	}
	b.challenge = next
	if b.onRedeem != nil {
		b.onRedeem()
	}
	return next.OperatorID, nil
}

func (b *fakeSAxess) approve(t *testing.T, operator string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	next, err := b.challenge.Decide(operator, b.challenge.BrowserTransactionID, b.challenge.Nonce, time.Now(), saxess.ChallengeApproved)
	if err != nil {
		t.Fatal(err)
	}
	b.challenge = next
}

func TestSAxessApprovalUsesBoundBrowserAndLocalMFA(t *testing.T) {
	cfg := &SAxessConfig{Backend: &fakeSAxess{}, ClientID: "bedrock-console", RedirectURI: testConsoleRedirect}
	h := newHarnessWithSAxess(t, methods.NewRateLimiter(100, time.Minute), nil, cfg)
	user := createLocalUser(t, h.client, "alice")
	spec := consoleRequestSpec()
	spec.ExpiresAt = metav1.NewTime(time.Now().Add(5 * time.Minute))
	create(t, h.client,
		&v1alpha1.OAuthClient{
			ObjectMeta: metav1.ObjectMeta{Name: spec.ClientID, Namespace: release.SystemNamespace},
			Spec:       consoleClientSpec(),
		},
		&v1alpha1.AuthRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "saxessauthrequest000000001", Namespace: release.SystemNamespace},
			Spec:       spec,
		},
		&v1alpha1.Group{
			ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.GroupAdmins, Namespace: release.SystemNamespace},
			Spec:       v1alpha1.GroupSpec{Members: []string{user.Name}, RequireSecondFactor: true},
		},
	)
	const id = "saxessauthrequest000000001"
	start := methods.Answer{Type: AnswerSAxessStart, Username: "alice"}
	poll := methods.Answer{Type: ChallengeSAxess}
	target := h.server.URL + "/api/v1/login/answer"
	cli := newBrowser(t, h.server)
	_, cliInitial := startLogin(t, h, cli)
	if cliInitial.SAxess {
		t.Fatal("CLI advertised sAxess")
	}
	if code := errorOf(t, send(t, cli, http.MethodPost, target, cliInitial.CSRF, start, nil), http.StatusBadRequest); code != "unexpected_answer" {
		t.Fatalf("CLI accepted sAxess start: %q", code)
	}
	browser := newBrowser(t, h.server)
	initial := challengeOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": id}, nil))
	if !initial.SAxess || initial.Type != methods.ChallengeUsername {
		t.Fatalf("eligible username challenge did not advertise sAxess: %+v", initial)
	}
	if code := errorOf(t, send(t, browser, http.MethodPost, target, "", start, nil), http.StatusForbidden); code != "csrf" {
		t.Fatalf("start without CSRF: %q", code)
	}
	pending := answerWith(t, h, browser, initial.CSRF, start)
	if pending.Type != ChallengeSAxess || pending.SAxess {
		t.Fatalf("start: %+v", pending)
	}
	if got := answerWith(t, h, browser, pending.CSRF, poll); got.Type != ChallengeSAxess || got.CSRF != pending.CSRF {
		t.Fatalf("pending poll: %+v", got)
	}
	backend := cfg.Backend.(*fakeSAxess)
	backend.approve(t, string(user.UID))
	stranger := newBrowser(t, h.server)
	if code := errorOf(t, send(t, stranger, http.MethodPost, target, pending.CSRF, poll, nil), http.StatusUnauthorized); code != "no_login" {
		t.Fatalf("unbound poll: %q", code)
	}
	backend.mu.Lock()
	backend.onRedeem = func() {
		// Simulate a concurrent challenge refresh after backend redemption but
		// before the approval's AuthRequest CAS.
		if _, err := h.store.SaveLogin(context.Background(), id, func(current v1alpha1.AuthRequest) v1alpha1.AuthRequest {
			current.Status.Login.CSRFHash = "rotated"
			return current
		}); err != nil {
			t.Errorf("rotate CSRF during redemption: %v", err)
		}
	}
	backend.mu.Unlock()
	if code := errorOf(t, send(t, browser, http.MethodPost, target, pending.CSRF, poll, nil), http.StatusForbidden); code != "csrf" {
		t.Fatalf("redemption committed over concurrent refresh: %q", code)
	}
	if status := storedRequest(t, h.client, id).Status; status.Subject != "" || status.Done {
		t.Fatalf("backend redemption alone advanced login: %+v", status)
	}
	reloaded := challengeOf(t, get(t, browser, h.server.URL+"/api/v1/login/challenge"))
	resp := send(t, browser, http.MethodPost, target, reloaded.CSRF, poll, nil)
	enroll := challengeOf(t, resp)
	if enroll.Type != methods.ChallengeTOTPEnroll || enroll.CSRF == pending.CSRF || enroll.Redirect != "" || enroll.Enroll == nil {
		t.Fatalf("approval did not require local MFA: %+v", enroll)
	}
	if cookieNamed(resp.Cookies(), CookieSession) != nil {
		t.Fatal("approval issued a session before local MFA")
	}
	status := storedRequest(t, h.client, id).Status
	if status.Subject != user.Name || status.Done || status.Session != "" || status.Login.Upstream != "" ||
		len(status.AMR) != 1 || status.AMR[0] != ChallengeSAxess {
		t.Fatalf("unexpected login status: %+v", status)
	}
	if code := errorOf(t, send(t, browser, http.MethodPost, target, pending.CSRF, poll, nil), http.StatusForbidden); code != "csrf" {
		t.Fatalf("replayed poll: %q", code)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enroll.Enroll.Secret)
	if err != nil {
		t.Fatal(err)
	}
	totp := methods.TOTPCode(seed, methods.TOTPStep(time.Now()))
	t.Run("disabled during MFA", func(t *testing.T) {
		deps := h.deps
		deps.SAxess = nil
		server := httptest.NewTLSServer(Handler(deps))
		defer server.Close()
		restarted := newBrowser(t, server)
		restarted.Jar = browser.Jar
		assertSAxessMFABlocked(t, h, restarted, server.URL, id, user.Name, enroll.CSRF, totp)
	})
	t.Run("demoted during MFA", func(t *testing.T) {
		ctx := context.Background()
		var admins v1alpha1.Group
		if err := h.client.Get(ctx, objectKey(v1alpha1.GroupAdmins), &admins); err != nil {
			t.Fatal(err)
		}
		admins.Spec.Members = nil
		if err := h.client.Update(ctx, &admins); err != nil {
			t.Fatal(err)
		}
		defer func() {
			admins.Spec.Members = []string{user.Name}
			if err := h.client.Update(ctx, &admins); err != nil {
				t.Fatal(err)
			}
		}()
		assertSAxessMFABlocked(t, h, browser, h.server.URL, id, user.Name, enroll.CSRF, totp)
	})
	totp = methods.TOTPCode(seed, methods.TOTPStep(time.Now()))
	codes := answerWith(t, h, browser, enroll.CSRF, methods.Answer{Type: methods.ChallengeTOTPEnroll, Code: totp})
	resp = send(t, browser, http.MethodPost, target, codes.CSRF, methods.Answer{Type: methods.ChallengeTOTPEnroll}, nil)
	done := challengeOf(t, resp)
	if done.Type != methods.ChallengeDone || done.Redirect != "/oauth/v2/authorize/callback?id="+id || cookieNamed(resp.Cookies(), CookieSession) == nil {
		t.Fatalf("local MFA did not finish the existing OAuth login: %+v", done)
	}
}

func assertSAxessMFABlocked(t *testing.T, h harness, browser *http.Client, baseURL, id, user, csrf, code string) {
	t.Helper()
	ctx := context.Background()
	key := objectKey(v1alpha1.CredentialName(user, v1alpha1.MethodTOTP))
	var before, after v1alpha1.Credential
	if err := h.client.Get(ctx, key, &before); err != nil {
		t.Fatal(err)
	}
	for _, response := range []*http.Response{
		get(t, browser, baseURL+"/api/v1/login/challenge"),
		send(t, browser, http.MethodPost, baseURL+"/api/v1/login/answer", csrf, methods.Answer{Type: methods.ChallengeTOTPEnroll, Code: code}, nil),
	} {
		got := challengeOf(t, response)
		if got.Type != methods.ChallengeErrorType || got.Error == nil || got.Error.Code != methods.FailureInvalidCredentials ||
			got.Redirect != "" || cookieNamed(response.Cookies(), CookieSession) != nil {
			t.Fatalf("revoked sAxess login accepted MFA: %+v", got)
		}
	}
	if err := h.client.Get(ctx, key, &after); err != nil {
		t.Fatal(err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Fatal("revoked sAxess login wrote TOTP credential")
	}
	if status := storedRequest(t, h.client, id).Status; status.Done || status.Session != "" {
		t.Fatalf("revoked sAxess login finished: %+v", status)
	}
}

func TestSAxessPollReportsExpired(t *testing.T) {
	backend := &fakeSAxess{}
	cfg := &SAxessConfig{Backend: backend, ClientID: "bedrock-console", RedirectURI: testConsoleRedirect}
	h := newHarnessWithSAxess(t, methods.NewRateLimiter(100, time.Minute), nil, cfg)
	user := createLocalUser(t, h.client, "alice")
	createLocalUser(t, h.client, "tenant")
	spec := consoleRequestSpec()
	spec.ExpiresAt = metav1.NewTime(time.Now().Add(5 * time.Minute))
	const id = "saxessexpiredrequest000001"
	create(t, h.client,
		&v1alpha1.OAuthClient{ObjectMeta: metav1.ObjectMeta{Name: spec.ClientID, Namespace: release.SystemNamespace}, Spec: consoleClientSpec()},
		&v1alpha1.AuthRequest{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: release.SystemNamespace}, Spec: spec},
		&v1alpha1.AuthRequest{ObjectMeta: metav1.ObjectMeta{Name: "saxesstenantrequest0000001", Namespace: release.SystemNamespace}, Spec: spec},
		&v1alpha1.Group{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.GroupAdmins, Namespace: release.SystemNamespace}, Spec: v1alpha1.GroupSpec{Members: []string{user.Name}}},
	)
	tenant := newBrowser(t, h.server)
	tenantStart := challengeOf(t, send(t, tenant, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": "saxesstenantrequest0000001"}, nil))
	rejected := answerWith(t, h, tenant, tenantStart.CSRF, methods.Answer{Type: AnswerSAxessStart, Username: "tenant"})
	if rejected.Type != methods.ChallengeUsername || rejected.Error == nil || rejected.Error.Code != methods.FailureInvalidCredentials {
		t.Fatalf("non-admin sAxess start: %+v", rejected)
	}
	backend.mu.Lock()
	nonce := backend.challenge.Nonce
	backend.mu.Unlock()
	if nonce != "" {
		t.Fatal("backend challenge created for a non-admin")
	}
	password := answerWith(t, h, tenant, rejected.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "tenant"})
	done := answerWith(t, h, tenant, password.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: testPassword})
	if done.Type != methods.ChallengeDone || done.Redirect == "" {
		t.Fatalf("non-admin password login was blocked: %+v", done)
	}
	browser := newBrowser(t, h.server)
	initial := challengeOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": id}, nil))
	pending := answerWith(t, h, browser, initial.CSRF, methods.Answer{Type: AnswerSAxessStart, Username: "alice"})
	if pending.Type != ChallengeSAxess {
		t.Fatalf("start: %+v", pending)
	}
	if _, err := h.store.SaveLogin(context.Background(), id, func(request v1alpha1.AuthRequest) v1alpha1.AuthRequest {
		nonce, _ := saxessPending(request.Status.Login)
		request.Status.Login.Upstream = nonce + "|" + time.Unix(0, 0).UTC().Format(time.RFC3339Nano)
		return request
	}); err != nil {
		t.Fatal(err)
	}
	got := answerWith(t, h, browser, pending.CSRF, methods.Answer{Type: ChallengeSAxess})
	if got.Type != methods.ChallengeErrorType || got.Error == nil || got.Error.Code != errorExpired {
		t.Fatalf("expired poll: %+v", got)
	}
}
