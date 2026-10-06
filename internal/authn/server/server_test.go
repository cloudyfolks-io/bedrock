package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	logs := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

func createUser(t *testing.T, c client.Client, username, password string) {
	t.Helper()
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.UserObjectName(username), Namespace: release.SystemNamespace},
		Spec:       v1alpha1.UserSpec{Username: username, Groups: []string{v1alpha1.GroupAdmins}, Methods: []string{v1alpha1.MethodPassword}},
	}
	if err := c.Create(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if err := methods.SetPassword(context.Background(), c, rand.Reader, *user, password); err != nil {
		t.Fatal(err)
	}
}

func readJSON[T any](t *testing.T, resp *http.Response, status int) T {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("status %d, want %d: %s", resp.StatusCode, status, body)
	}
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return value
}

func discoveryOf(t *testing.T, server *httptest.Server, host string) map[string]any {
	t.Helper()
	return readJSON[map[string]any](t, request(t, server.Client(), server, http.MethodGet, host, "/.well-known/openid-configuration", nil, nil), http.StatusOK)
}

func stringsOf(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, _ := item.(string)
		out = append(out, text)
	}
	return out
}

func TestDiscoveryPaths(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	key := saveSigningKey(t, c, time.Now().Add(-time.Minute))
	server := newTestServer(t, cfg, time.Now)
	discovery := discoveryOf(t, server, "sso."+testHost)
	want := map[string]string{
		"issuer":                        testIssuer,
		"authorization_endpoint":        testIssuer + "/oauth/v2/authorize",
		"token_endpoint":                testIssuer + "/oauth/v2/token",
		"device_authorization_endpoint": testIssuer + "/oauth/v2/device_authorization",
		"introspection_endpoint":        testIssuer + "/oauth/v2/introspect",
		"revocation_endpoint":           testIssuer + "/oauth/v2/revoke",
		"end_session_endpoint":          testIssuer + "/oidc/v1/end_session",
		"jwks_uri":                      testIssuer + "/oauth/v2/keys",
		"userinfo_endpoint":             testIssuer + "/oidc/v1/userinfo",
	}
	for field, value := range want {
		if discovery[field] != value {
			t.Fatalf("%s = %v, want %s", field, discovery[field], value)
		}
	}
	grants := stringsOf(discovery["grant_types_supported"])
	for _, grant := range []string{v1alpha1.GrantAuthorizationCode, v1alpha1.GrantRefreshToken, v1alpha1.GrantDeviceCode, v1alpha1.GrantTokenExchange} {
		if !slices.Contains(grants, grant) {
			t.Fatalf("grant %s missing from %v", grant, grants)
		}
	}
	if !slices.Contains(stringsOf(discovery["code_challenge_methods_supported"]), "S256") {
		t.Fatalf("code challenge methods %v", discovery["code_challenge_methods_supported"])
	}
	if got := stringsOf(discovery["ui_locales_supported"]); !reflect.DeepEqual(got, []string{"en", "fa", "ar"}) {
		t.Fatalf("ui locales %v", got)
	}
	scopes := stringsOf(discovery["scopes_supported"])
	if !slices.Contains(scopes, "groups") || !slices.Contains(scopes, "offline_access") {
		t.Fatalf("scopes %v", scopes)
	}
	jwks := readJSON[struct {
		Keys []struct {
			KID string `json:"kid"`
			Alg string `json:"alg"`
		} `json:"keys"`
	}](t, request(t, server.Client(), server, http.MethodGet, "sso."+testHost, "/oauth/v2/keys", nil, nil), http.StatusOK)
	if len(jwks.Keys) != 1 || jwks.Keys[0].KID != key.ID || jwks.Keys[0].Alg != "ES256" {
		t.Fatalf("jwks %+v", jwks)
	}
}

func TestServerRefusesAnOtherHost(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	server := newTestServer(t, cfg, time.Now)
	for _, host := range []string{"evil.test", testHost, "sso.example.test.evil.test", "SSO.example.test", "sso.other.test"} {
		for _, target := range []string{"/.well-known/openid-configuration", "/login/", "/api/v1/cluster", "/api/v1/account"} {
			resp := request(t, server.Client(), server, http.MethodGet, host, target, nil, nil)
			if code := readJSON[map[string]string](t, resp, http.StatusMisdirectedRequest)["error"]; code != "misdirected_request" {
				t.Fatalf("%s%s: %q", host, target, code)
			}
		}
	}
	for _, target := range []string{"/healthz", "/readyz"} {
		if resp := request(t, server.Client(), server, http.MethodGet, "evil.test", target, nil, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s on another host: %d", target, resp.StatusCode)
		}
	}
	webhook := request(t, server.Client(), server, http.MethodPost, "evil.test", "/webhook/v1/tokenreview", strings.NewReader("{}"), nil)
	if webhook.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the webhook is reached on any host and still needs its bearer: %d", webhook.StatusCode)
	}
	withPort := request(t, server.Client(), server, http.MethodGet, "sso.example.test:443", "/.well-known/openid-configuration", nil, nil)
	if withPort.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("a host with a port would put the port into the issuer and must be refused: %d", withPort.StatusCode)
	}
}

func TestIssuerFollowsPlatformHost(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	server := newTestServer(t, cfg, time.Now)
	if issuer := discoveryOf(t, server, "sso."+testHost)["issuer"]; issuer != testIssuer {
		t.Fatalf("issuer %v", issuer)
	}
	setSetting(t, c, "platform.host", "other.test")
	old := request(t, server.Client(), server, http.MethodGet, "sso."+testHost, "/.well-known/openid-configuration", nil, nil)
	if old.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("the old host must be refused at once: %d", old.StatusCode)
	}
	moved := discoveryOf(t, server, "sso.other.test")
	if moved["issuer"] != "https://sso.other.test" || moved["jwks_uri"] != "https://sso.other.test/oauth/v2/keys" {
		t.Fatalf("moved discovery %v", moved)
	}
	setSetting(t, c, "platform.host", "")
	if issuer := discoveryOf(t, server, "sso.10-0-0-10.sslip.io")["issuer"]; issuer != "https://sso.10-0-0-10.sslip.io" {
		t.Fatalf("an empty platform.host follows the VIP: %v", issuer)
	}
}

func TestAuthorizeRedirectsToLogin(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	server := newTestServer(t, cfg, time.Now)
	browser := noRedirects(server, nil)
	query := url.Values{
		"client_id":             {"bedrock-cli"},
		"redirect_uri":          {testRedirect},
		"response_type":         {"code"},
		"scope":                 {"openid offline_access"},
		"state":                 {"s1"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
	}
	resp := request(t, browser, server, http.MethodGet, "sso."+testHost, "/oauth/v2/authorize?"+query.Encode(), nil, nil)
	match := regexp.MustCompile(`^/login/\?authRequest=([0-9a-z]{26})$`).FindStringSubmatch(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || match == nil {
		t.Fatalf("authorize: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var stored v1alpha1.AuthRequest
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: release.SystemNamespace, Name: match[1]}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.ClientID != "bedrock-cli" || stored.Spec.RedirectURI != testRedirect || stored.Spec.CodeChallengeMethod != "S256" {
		t.Fatalf("stored request %+v", stored.Spec)
	}
	evil := url.Values{}
	for key, values := range query {
		evil[key] = values
	}
	evil.Set("redirect_uri", "https://evil.test/callback")
	refused := request(t, browser, server, http.MethodGet, "sso."+testHost, "/oauth/v2/authorize?"+evil.Encode(), nil, nil)
	if refused.StatusCode == http.StatusFound || strings.HasPrefix(refused.Header.Get("Location"), "https://evil.test") {
		t.Fatalf("an unregistered redirect must not be followed: %d %q", refused.StatusCode, refused.Header.Get("Location"))
	}
}

func TestClusterInfoEndpoint(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	server := newTestServer(t, cfg, time.Now)
	info := readJSON[map[string]string](t, request(t, server.Client(), server, http.MethodGet, "sso."+testHost, "/api/v1/cluster", nil, nil), http.StatusOK)
	if !reflect.DeepEqual(info, map[string]string{"server": "https://api.example.test:6443", "certificateAuthority": testCA}) {
		t.Fatalf("cluster info %v", info)
	}
	if err := c.Delete(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	missing := request(t, server.Client(), server, http.MethodGet, "sso."+testHost, "/api/v1/cluster", nil, nil)
	if code := readJSON[map[string]string](t, missing, http.StatusServiceUnavailable)["error"]; code != "cluster_ca_unavailable" {
		t.Fatalf("missing CA: %q", code)
	}
}

func TestReadyNeedsASigningKey(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	clock := &testClock{now: time.Now()}
	server := newTestServer(t, cfg, clock.Now)
	status := func(target string) int {
		return request(t, server.Client(), server, http.MethodGet, "sso."+testHost, target, nil, nil).StatusCode
	}
	if got := status("/healthz"); got != http.StatusOK {
		t.Fatalf("healthz %d", got)
	}
	if got := status("/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("readyz without a key %d", got)
	}
	saveSigningKey(t, c, clock.Now().Add(-time.Minute))
	if got := status("/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("the key cache holds for 30 s, readyz %d", got)
	}
	clock.Advance(31 * time.Second)
	if got := status("/readyz"); got != http.StatusOK {
		t.Fatalf("readyz with a key %d", got)
	}
}

func postJSON(t *testing.T, browser *http.Client, server *httptest.Server, target, csrf string, body any) (methods.Challenge, *http.Response) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{"Content-Type": {"application/json"}}
	if csrf != "" {
		header.Set("X-CSRF-Token", csrf)
	}
	resp := request(t, browser, server, http.MethodPost, "sso."+testHost, target, bytes.NewReader(payload), header)
	return readJSON[methods.Challenge](t, resp, http.StatusOK), resp
}

func cookieValues(resp *http.Response) []string {
	var values []string
	for _, cookie := range resp.Cookies() {
		values = append(values, cookie.Value)
	}
	return values
}

func TestServerLogsNoSecrets(t *testing.T) {
	logs := captureLogs(t)
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	password := "correct horse battery staple"
	wrong := "wrong horse battery staple"
	createUser(t, c, "admin", password)
	server := newTestServer(t, cfg, time.Now)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := noRedirects(server, jar)
	host := "sso." + testHost
	verifier := strings.Repeat("0123456789", 5)
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {"bedrock-cli"},
		"redirect_uri":          {testRedirect},
		"response_type":         {"code"},
		"scope":                 {"openid offline_access groups"},
		"state":                 {"s1"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	authorize := request(t, browser, server, http.MethodGet, host, "/oauth/v2/authorize?"+query.Encode(), nil, nil)
	loginURL, err := url.Parse(authorize.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{password, wrong, verifier}
	step, resp := postJSON(t, browser, server, "/api/v1/login/start", "", map[string]string{"authRequest": loginURL.Query().Get("authRequest")})
	secrets = append(append(secrets, step.CSRF), cookieValues(resp)...)
	step, _ = postJSON(t, browser, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "admin"})
	secrets = append(secrets, step.CSRF)
	step, _ = postJSON(t, browser, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: wrong})
	if step.Error == nil || step.Error.Code != methods.FailureInvalidCredentials {
		t.Fatalf("wrong password %+v", step)
	}
	secrets = append(secrets, step.CSRF)
	step, resp = postJSON(t, browser, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: password})
	if step.Type != methods.ChallengeDone || !strings.HasPrefix(step.Redirect, testIssuer+"/oauth/v2/authorize/callback?id=") {
		t.Fatalf("done %+v", step)
	}
	secrets = append(secrets, cookieValues(resp)...)
	callbackURL, err := url.Parse(step.Redirect)
	if err != nil {
		t.Fatal(err)
	}
	callback := request(t, browser, server, http.MethodGet, host, callbackURL.RequestURI(), nil, nil)
	location, err := url.Parse(callback.Header.Get("Location"))
	if err != nil || callback.StatusCode != http.StatusFound || !strings.HasPrefix(location.String(), testRedirect) || location.Query().Get("state") != "s1" {
		t.Fatalf("callback %d %q", callback.StatusCode, callback.Header.Get("Location"))
	}
	code := location.Query().Get("code")
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {testRedirect}, "client_id": {"bedrock-cli"}, "code_verifier": {verifier}}
	formHeader := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	tokens := readJSON[map[string]any](t, request(t, browser, server, http.MethodPost, host, "/oauth/v2/token", strings.NewReader(form.Encode()), formHeader), http.StatusOK)
	access, _ := tokens["access_token"].(string)
	refresh, _ := tokens["refresh_token"].(string)
	idToken, _ := tokens["id_token"].(string)
	if access == "" || refresh == "" || idToken == "" {
		t.Fatalf("tokens %v", tokens)
	}
	claims := jwtClaims(t, access)
	if claims["sub"] != "admin" || claims["iss"] != testIssuer || !slices.Contains(stringsOf(claims["aud"]), "bedrock") || !slices.Contains(stringsOf(claims["groups"]), v1alpha1.GroupAdmins) {
		t.Fatalf("access token claims %v", claims)
	}
	secrets = append(secrets, code, access, refresh, idToken)
	form.Set("code_verifier", "not-the-verifier-not-the-verifier-not-the-verifier")
	replay := request(t, browser, server, http.MethodPost, host, "/oauth/v2/token", strings.NewReader(form.Encode()), formHeader)
	if replay.StatusCode != http.StatusBadRequest {
		t.Fatalf("a used code must fail: %d", replay.StatusCode)
	}
	otherJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	other := noRedirects(server, otherJar)
	again := request(t, other, server, http.MethodGet, host, "/oauth/v2/authorize?"+query.Encode(), nil, nil)
	againURL, err := url.Parse(again.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	step, resp = postJSON(t, other, server, "/api/v1/login/start", "", map[string]string{"authRequest": againURL.Query().Get("authRequest")})
	secrets = append(append(secrets, step.CSRF), cookieValues(resp)...)
	step, _ = postJSON(t, other, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "admin"})
	secrets = append(secrets, step.CSRF)
	if err := c.Delete(context.Background(), &v1alpha1.OAuthClient{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-cli", Namespace: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(methods.Answer{Type: methods.ChallengePassword, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	failed := request(t, other, server, http.MethodPost, host, "/api/v1/login/answer", bytes.NewReader(payload), http.Header{"Content-Type": {"application/json"}, "X-Csrf-Token": {step.CSRF}})
	if code := readJSON[map[string]string](t, failed, http.StatusInternalServerError)["error"]; code != "internal" {
		t.Fatalf("a login without its client: %q", code)
	}
	if err := c.Delete(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	request(t, browser, server, http.MethodGet, host, "/api/v1/cluster", nil, nil)
	written := logs.String()
	for index, value := range secrets {
		if value != "" && strings.Contains(written, value) {
			t.Fatalf("secret %d reached the log", index)
		}
	}
	for _, cause := range []string{"login request failed", "cluster_ca_unavailable"} {
		if !strings.Contains(written, cause) {
			t.Fatalf("the log misses %q: %s", cause, written)
		}
	}
}

func TestServerLogsNoFactorClientOrTokenSecrets(t *testing.T) {
	logs := captureLogs(t)
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	password := "correct horse battery staple"
	createUser(t, c, "admin", password)
	clientSecret := "billing-client-secret-0123456789abcdef"
	wrongSecret := "billing-client-secret-not-the-right-one"
	if err := c.Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "billing-client", Namespace: release.SystemNamespace}, Data: map[string][]byte{"clientSecret": []byte(clientSecret)}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Name: "billing", Namespace: release.SystemNamespace},
		Spec: v1alpha1.OAuthClientSpec{
			ClientID:      "billing",
			GrantTypes:    []string{v1alpha1.GrantTokenExchange},
			TokenExchange: &v1alpha1.TokenExchange{Audiences: []string{"billing-api"}},
			SecretRef:     "billing-client",
		},
	}); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, cfg, time.Now)
	host := "sso." + testHost
	secrets := []string{password, clientSecret, wrongSecret, testBearer}
	enroller := newBrowser(t, server)
	if done := passwordLogin(t, server, enroller, "admin", password); done.Type != methods.ChallengeDone {
		t.Fatalf("login %+v", done)
	}
	csrf := accountCSRF(t, server, enroller)
	enrollment := readJSON[methods.TOTPEnrollment](t, accountPost(t, server, enroller, csrf, "/api/v1/account/totp", "{}"), http.StatusOK)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatal(err)
	}
	step := methods.TOTPStep(time.Now())
	valid := []string{methods.TOTPCode(seed, step-1), methods.TOTPCode(seed, step), methods.TOTPCode(seed, step+1), methods.TOTPCode(seed, step+2)}
	guess := "000000"
	if slices.Contains(valid, guess) {
		guess = "111111"
	}
	secrets = append(append(secrets, enrollment.Secret, enrollment.OTPAuthURL, guess, "zzzzz-zzzzz"), valid...)
	readJSON[map[string]string](t, accountPost(t, server, enroller, csrf, "/api/v1/account/totp/verify", `{"code":"`+guess+`"}`), http.StatusForbidden)
	codes := readJSON[map[string][]string](t, accountPost(t, server, enroller, csrf, "/api/v1/account/totp/verify", `{"code":"`+methods.TOTPCode(seed, step)+`"}`), http.StatusOK)["recoveryCodes"]
	if len(codes) == 0 {
		t.Fatal("no recovery codes")
	}
	secrets = append(secrets, codes...)
	created := readJSON[map[string]string](t, accountPost(t, server, enroller, csrf, "/api/v1/account/tokens", `{"description":"ci"}`), http.StatusCreated)
	apiToken := created["token"]
	wrongToken := "brk_" + strings.Repeat("Z", 40)
	secrets = append(secrets, apiToken, wrongToken)
	browser := newBrowser(t, server)
	challenge := passwordLogin(t, server, browser, "admin", password)
	if challenge.Type != methods.ChallengeTOTP {
		t.Fatalf("second factor %+v", challenge)
	}
	challenge, _ = postJSON(t, browser, server, "/api/v1/login/answer", challenge.CSRF, methods.Answer{Type: methods.ChallengeTOTP, Code: guess})
	challenge, _ = postJSON(t, browser, server, "/api/v1/login/answer", challenge.CSRF, methods.Answer{Type: methods.ChallengeRecovery, Code: "zzzzz-zzzzz"})
	done, _ := postJSON(t, browser, server, "/api/v1/login/answer", challenge.CSRF, methods.Answer{Type: methods.ChallengeRecovery, Code: codes[0]})
	if done.Type != methods.ChallengeDone {
		t.Fatalf("recovery login %+v", done)
	}
	callbackURL, err := url.Parse(done.Redirect)
	if err != nil {
		t.Fatal(err)
	}
	callback := request(t, browser, server, http.MethodGet, host, callbackURL.RequestURI(), nil, nil)
	location, err := url.Parse(callback.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	formHeader := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	tokens := readJSON[map[string]any](t, request(t, server.Client(), server, http.MethodPost, host, "/oauth/v2/token", strings.NewReader(url.Values{"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")}, "redirect_uri": {testRedirect}, "client_id": {"bedrock-cli"}, "code_verifier": {"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"}}.Encode()), formHeader), http.StatusOK)
	access, _ := tokens["access_token"].(string)
	secrets = append(secrets, access)
	confidential := func(secret string) http.Header {
		header := formHeader.Clone()
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("billing:"+secret)))
		return header
	}
	exchange := url.Values{"grant_type": {v1alpha1.GrantTokenExchange}, "subject_token": {access}, "subject_token_type": {"urn:ietf:params:oauth:token-type:access_token"}, "audience": {"billing-api"}}
	exchanged := readJSON[map[string]any](t, request(t, server.Client(), server, http.MethodPost, host, "/oauth/v2/token", strings.NewReader(exchange.Encode()), confidential(clientSecret)), http.StatusOK)
	exchangedToken, _ := exchanged["access_token"].(string)
	if exchangedToken == "" {
		t.Fatalf("exchange %v", exchanged)
	}
	secrets = append(secrets, exchangedToken)
	if resp := request(t, server.Client(), server, http.MethodPost, host, "/oauth/v2/token", strings.NewReader(exchange.Encode()), confidential(wrongSecret)); resp.StatusCode == http.StatusOK {
		t.Fatal("a wrong client secret must be refused")
	}
	introspection := readJSON[map[string]any](t, request(t, server.Client(), server, http.MethodPost, host, "/oauth/v2/introspect", strings.NewReader(url.Values{"token": {access}}.Encode()), confidential(clientSecret)), http.StatusOK)
	if introspection["active"] != true {
		t.Fatalf("introspection %v", introspection)
	}
	review := func(bearer, token string) *http.Response {
		body := `{"apiVersion":"authentication.k8s.io/v1","kind":"TokenReview","spec":{"token":"` + token + `"}}`
		return request(t, server.Client(), server, http.MethodPost, host, "/webhook/v1/tokenreview", strings.NewReader(body), http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + bearer}})
	}
	accepted := readJSON[map[string]any](t, review(testBearer, apiToken), http.StatusOK)
	if status, _ := accepted["status"].(map[string]any); status["authenticated"] != true {
		t.Fatalf("token review %v", accepted)
	}
	readJSON[map[string]any](t, review(testBearer, wrongToken), http.StatusOK)
	if resp := review(wrongSecret, apiToken); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong webhook bearer: %d", resp.StatusCode)
	}
	written := logs.String()
	if written == "" {
		t.Fatal("the flows must log something")
	}
	for index, value := range secrets {
		if value != "" && strings.Contains(written, value) {
			t.Fatalf("secret %d reached the log", index)
		}
	}
}

func jwtClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func passwordLogin(t *testing.T, server *httptest.Server, browser *http.Client, username, password string) methods.Challenge {
	t.Helper()
	query := url.Values{
		"client_id":             {"bedrock-cli"},
		"redirect_uri":          {testRedirect},
		"response_type":         {"code"},
		"scope":                 {"openid offline_access"},
		"state":                 {"s1"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
	}
	authorize := request(t, browser, server, http.MethodGet, "sso."+testHost, "/oauth/v2/authorize?"+query.Encode(), nil, nil)
	loginURL, err := url.Parse(authorize.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	step, _ := postJSON(t, browser, server, "/api/v1/login/start", "", map[string]string{"authRequest": loginURL.Query().Get("authRequest")})
	step, _ = postJSON(t, browser, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: username})
	step, _ = postJSON(t, browser, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: password})
	return step
}

func newBrowser(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return noRedirects(server, jar)
}

func TestAuthorizeCallbackNeedsTheLoginBrowser(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	password := "correct horse battery staple"
	createUser(t, c, "admin", password)
	server := newTestServer(t, cfg, time.Now)
	host := "sso." + testHost
	owner := newBrowser(t, server)
	done := passwordLogin(t, server, owner, "admin", password)
	other := newBrowser(t, server)
	otherDone := passwordLogin(t, server, other, "admin", password)
	if done.Type != methods.ChallengeDone || otherDone.Type != methods.ChallengeDone {
		t.Fatalf("logins %+v %+v", done, otherDone)
	}
	callbackOf := func(step methods.Challenge) string {
		parsed, err := url.Parse(step.Redirect)
		if err != nil {
			t.Fatal(err)
		}
		return parsed.RequestURI()
	}
	refused := func(browser *http.Client, header http.Header) {
		t.Helper()
		resp := request(t, browser, server, http.MethodGet, host, callbackOf(done), nil, header)
		if code := readJSON[map[string]string](t, resp, http.StatusUnauthorized)["error"]; code != "no_session" {
			t.Fatalf("callback from another browser: %q", code)
		}
	}
	refused(noRedirects(server, nil), nil)
	refused(noRedirects(server, nil), http.Header{"Cookie": {login.CookieSession + "=forged"}})
	refused(other, nil)
	completes := func(browser *http.Client, step methods.Challenge) {
		t.Helper()
		resp := request(t, browser, server, http.MethodGet, host, callbackOf(step), nil, nil)
		location, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || resp.StatusCode != http.StatusFound || !strings.HasPrefix(location.String(), testRedirect) || location.Query().Get("code") == "" {
			t.Fatalf("callback in the login browser: %d %q", resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	completes(owner, done)
	completes(other, otherDone)
}

func TestCrossSiteRequestsAreRefused(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	server := newTestServer(t, cfg, time.Now)
	host := "sso." + testHost
	guarded := []struct{ method, target string }{
		{http.MethodPost, "/api/v1/login/start"},
		{http.MethodPost, "/api/v1/login/device"},
		{http.MethodGet, "/api/v1/login/challenge"},
		{http.MethodPost, "/api/v1/login/answer"},
		{http.MethodPost, "/api/v1/account/password"},
		{http.MethodPost, "/api/v1/account/totp"},
		{http.MethodPost, "/api/v1/account/totp/verify"},
		{http.MethodDelete, "/api/v1/account/totp"},
		{http.MethodPost, "/api/v1/account/recovery-codes"},
		{http.MethodPost, "/api/v1/account/tokens"},
		{http.MethodDelete, "/api/v1/account/tokens/abc"},
		{http.MethodDelete, "/api/v1/account/sessions/abc"},
		{http.MethodPost, "/api/v1/account/logout"},
	}
	send := func(method, target, site string) *http.Response {
		header := http.Header{"Content-Type": {"application/json"}}
		if site != "" {
			header.Set("Sec-Fetch-Site", site)
		}
		return request(t, server.Client(), server, method, host, target, strings.NewReader("{}"), header)
	}
	for _, route := range guarded {
		if code := readJSON[map[string]string](t, send(route.method, route.target, "cross-site"), http.StatusForbidden)["error"]; code != "csrf" {
			t.Fatalf("cross-site %s %s: %q", route.method, route.target, code)
		}
		for _, site := range []string{"", "same-origin", "same-site", "none"} {
			if resp := send(route.method, route.target, site); resp.StatusCode == http.StatusForbidden {
				t.Fatalf("%q %s %s was refused", site, route.method, route.target)
			}
		}
	}
	for _, target := range []string{"/api/v1/account", "/api/v1/account/sessions", "/api/v1/login/providers/corp/callback?state=x&code=y"} {
		if resp := send(http.MethodGet, target, "cross-site"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("cross-site read %s: %d", target, resp.StatusCode)
		}
	}
}

func accountCSRF(t *testing.T, server *httptest.Server, browser *http.Client) string {
	t.Helper()
	return readJSON[struct {
		CSRF string `json:"csrf"`
	}](t, request(t, browser, server, http.MethodGet, "sso."+testHost, "/api/v1/account", nil, nil), http.StatusOK).CSRF
}

func accountPost(t *testing.T, server *httptest.Server, browser *http.Client, csrf, target, body string) *http.Response {
	t.Helper()
	header := http.Header{"Content-Type": {"application/json"}, "X-Csrf-Token": {csrf}}
	return request(t, browser, server, http.MethodPost, "sso."+testHost, target, strings.NewReader(body), header)
}

func TestRateLimitCountsEachPasswordLoginOnce(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	password := "correct horse battery staple"
	createUser(t, c, "admin", password)
	server := newTestServer(t, cfg, time.Now)
	first := newBrowser(t, server)
	for attempt := 1; attempt <= attemptsPerMinute; attempt++ {
		browser := first
		if attempt > 1 {
			browser = newBrowser(t, server)
		}
		if done := passwordLogin(t, server, browser, "admin", password); done.Type != methods.ChallengeDone {
			t.Fatalf("password login %d of %d per minute: %+v", attempt, attemptsPerMinute, done)
		}
	}
	limited := passwordLogin(t, server, newBrowser(t, server), "admin", password)
	if limited.Error == nil || limited.Error.Code != methods.FailureRateLimited {
		t.Fatalf("login %d: %+v", attemptsPerMinute+1, limited)
	}
	csrf := accountCSRF(t, server, first)
	resp := accountPost(t, server, first, csrf, "/api/v1/account/password", `{"current":"`+password+`","new":"another horse battery staple"}`)
	if code := readJSON[map[string]string](t, resp, http.StatusTooManyRequests)["error"]; code != methods.FailureRateLimited {
		t.Fatalf("the account API shares the login budget: %q", code)
	}
}

func TestCredentialBudgetIsPerUserAndAddress(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	password := "correct horse battery staple"
	createUser(t, c, "admin", password)
	createUser(t, c, "operator", password)
	server := newTestServer(t, cfg, time.Now)
	for attempt := 1; attempt <= attemptsPerMinute; attempt++ {
		if done := passwordLogin(t, server, newBrowser(t, server), "admin", "wrong horse battery staple"); done.Error == nil || done.Error.Code != methods.FailureInvalidCredentials {
			t.Fatalf("wrong admin password %d: %+v", attempt, done)
		}
	}
	if limited := passwordLogin(t, server, newBrowser(t, server), "admin", password); limited.Error == nil || limited.Error.Code != methods.FailureRateLimited {
		t.Fatalf("admin after %d attempts: %+v", attemptsPerMinute, limited)
	}
	operator := newBrowser(t, server)
	if done := passwordLogin(t, server, operator, "operator", password); done.Type != methods.ChallengeDone {
		t.Fatalf("another user from the same address must keep its own budget: %+v", done)
	}
	csrf := accountCSRF(t, server, operator)
	resp := accountPost(t, server, operator, csrf, "/api/v1/account/password", `{"current":"wrong horse battery staple","new":"another horse battery staple"}`)
	if code := readJSON[map[string]string](t, resp, http.StatusForbidden)["error"]; code != methods.FailureInvalidCredentials {
		t.Fatalf("the account check of another user: %q", code)
	}
	if limited := passwordLogin(t, server, newBrowser(t, server), "admin", password); limited.Error == nil || limited.Error.Code != methods.FailureRateLimited {
		t.Fatalf("admin must stay limited: %+v", limited)
	}
}

func TestOneRateLimiterServesLoginAndAccount(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	password := "correct horse battery staple"
	createUser(t, c, "admin", password)
	server := newTestServer(t, cfg, time.Now)
	browser := newBrowser(t, server)
	if done := passwordLogin(t, server, browser, "admin", password); done.Type != methods.ChallengeDone {
		t.Fatalf("login %+v", done)
	}
	csrf := accountCSRF(t, server, browser)
	change := func(status int) string {
		t.Helper()
		resp := accountPost(t, server, browser, csrf, "/api/v1/account/password", `{"current":"wrong horse battery staple","new":"another horse battery staple"}`)
		return readJSON[map[string]string](t, resp, status)["error"]
	}
	for attempt := 2; attempt <= attemptsPerMinute; attempt++ {
		if code := change(http.StatusForbidden); code != methods.FailureInvalidCredentials {
			t.Fatalf("attempt %d: %q", attempt, code)
		}
	}
	if code := change(http.StatusTooManyRequests); code != methods.FailureRateLimited {
		t.Fatalf("the login must count against the account API: %q", code)
	}
	limited := passwordLogin(t, server, newBrowser(t, server), "admin", password)
	if limited.Error == nil || limited.Error.Code != methods.FailureRateLimited {
		t.Fatalf("the account attempts must count against the login API: %+v", limited)
	}
}

func TestSecondFactorGuessesAreRateLimited(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	password := "correct horse battery staple"
	createUser(t, c, "admin", password)
	server := newTestServer(t, cfg, time.Now)
	enroller := newBrowser(t, server)
	if done := passwordLogin(t, server, enroller, "admin", password); done.Type != methods.ChallengeDone {
		t.Fatalf("login %+v", done)
	}
	csrf := accountCSRF(t, server, enroller)
	enrollment := readJSON[methods.TOTPEnrollment](t, accountPost(t, server, enroller, csrf, "/api/v1/account/totp", "{}"), http.StatusOK)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatal(err)
	}
	step := methods.TOTPStep(time.Now())
	code := methods.TOTPCode(seed, step)
	valid := []string{methods.TOTPCode(seed, step-1), code, methods.TOTPCode(seed, step+1), methods.TOTPCode(seed, step+2)}
	guess := "000000"
	if slices.Contains(valid, guess) {
		guess = "111111"
	}
	readJSON[map[string][]string](t, accountPost(t, server, enroller, csrf, "/api/v1/account/totp/verify", `{"code":"`+code+`"}`), http.StatusOK)
	browser := newBrowser(t, server)
	challenge := passwordLogin(t, server, browser, "admin", password)
	if challenge.Type != methods.ChallengeTOTP {
		t.Fatalf("second factor challenge %+v", challenge)
	}
	attempt := 4
	guessIn := func(browser *http.Client, challenge methods.Challenge, guesses int) methods.Challenge {
		t.Helper()
		for range guesses {
			attempt++
			challenge, _ = postJSON(t, browser, server, "/api/v1/login/answer", challenge.CSRF, methods.Answer{Type: methods.ChallengeTOTP, Code: guess})
			limited := challenge.Error != nil && challenge.Error.Code == methods.FailureRateLimited
			if challenge.Type != methods.ChallengeTOTP || limited != (attempt > attemptsPerMinute) {
				t.Fatalf("TOTP guess at attempt %d: %+v", attempt, challenge)
			}
		}
		return challenge
	}
	guessIn(browser, challenge, 4)
	again := newBrowser(t, server)
	challenge = passwordLogin(t, server, again, "admin", password)
	attempt++
	if challenge.Type != methods.ChallengeTOTP {
		t.Fatalf("second login %+v", challenge)
	}
	guessIn(again, challenge, attemptsPerMinute+1-attempt)
}

func authorizeQuery() url.Values {
	return url.Values{
		"client_id":             {"bedrock-cli"},
		"redirect_uri":          {testRedirect},
		"response_type":         {"code"},
		"scope":                 {"openid offline_access"},
		"state":                 {"s1"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
	}
}

func TestFlowStartsAreRateLimitedPerClientIP(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	password := "correct horse battery staple"
	createUser(t, c, "admin", password)
	server := newTestServer(t, cfg, time.Now)
	host := "sso." + testHost
	browser := newBrowser(t, server)
	authorize := request(t, browser, server, http.MethodGet, host, "/oauth/v2/authorize?"+authorizeQuery().Encode(), nil, nil)
	loginURL, err := url.Parse(authorize.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	step, _ := postJSON(t, browser, server, "/api/v1/login/start", "", map[string]string{"authRequest": loginURL.Query().Get("authRequest")})
	step, _ = postJSON(t, browser, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "admin"})
	anonymous := noRedirects(server, nil)
	for attempt := 2; attempt <= startsPerMinute; attempt++ {
		if resp := request(t, anonymous, server, http.MethodGet, host, "/oauth/v2/authorize?"+authorizeQuery().Encode(), nil, nil); resp.StatusCode != http.StatusFound {
			t.Fatalf("authorize %d of %d: %d", attempt, startsPerMinute, resp.StatusCode)
		}
	}
	limited := request(t, anonymous, server, http.MethodGet, host, "/oauth/v2/authorize?"+authorizeQuery().Encode(), nil, nil)
	if code := readJSON[map[string]string](t, limited, http.StatusTooManyRequests)["error"]; code != methods.FailureRateLimited {
		t.Fatalf("authorize over the budget: %q", code)
	}
	formHeader := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	device := request(t, anonymous, server, http.MethodPost, host, "/oauth/v2/device_authorization", strings.NewReader(url.Values{"client_id": {"bedrock-cli"}, "scope": {"openid"}}.Encode()), formHeader)
	if code := readJSON[map[string]string](t, device, http.StatusTooManyRequests)["error"]; code != methods.FailureRateLimited {
		t.Fatalf("device authorization over the budget: %q", code)
	}
	callback := request(t, anonymous, server, http.MethodGet, host, "/oauth/v2/authorize/callback", nil, nil)
	if code := readJSON[map[string]string](t, callback, http.StatusBadRequest)["error"]; code != "invalid_request" {
		t.Fatalf("the authorize callback is outside the budget: %q", code)
	}
	elsewhere := request(t, anonymous, server, http.MethodGet, host, "/oauth/v2/authorize?"+authorizeQuery().Encode(), nil, http.Header{"X-Forwarded-For": {"198.51.100.9"}})
	if elsewhere.StatusCode != http.StatusFound {
		t.Fatalf("another address has its own budget: %d", elsewhere.StatusCode)
	}
	done, _ := postJSON(t, browser, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: password})
	if done.Type != methods.ChallengeDone {
		t.Fatalf("the flow budget must not spend the credential budget: %+v", done)
	}
}

func audienceOf(claims map[string]any) []string {
	if single, ok := claims["aud"].(string); ok {
		return []string{single}
	}
	return stringsOf(claims["aud"])
}

func checkTokenAudiences(t *testing.T, flow string, tokens map[string]any) {
	t.Helper()
	access, _ := tokens["access_token"].(string)
	idToken, _ := tokens["id_token"].(string)
	if access == "" || idToken == "" {
		t.Fatalf("%s tokens %v", flow, tokens)
	}
	if got := audienceOf(jwtClaims(t, access)); !reflect.DeepEqual(got, []string{"bedrock", "bedrock-cli"}) {
		t.Fatalf("%s access token audience %v", flow, got)
	}
	if got := audienceOf(jwtClaims(t, idToken)); !reflect.DeepEqual(got, []string{"bedrock-cli"}) {
		t.Fatalf("%s ID token audience %v", flow, got)
	}
}

func TestIDTokensNameOnlyTheClient(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	createCLIClient(t, c)
	password := "correct horse battery staple"
	createUser(t, c, "admin", password)
	server := newTestServer(t, cfg, time.Now)
	host := "sso." + testHost
	formHeader := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	token := func(form url.Values) map[string]any {
		t.Helper()
		return readJSON[map[string]any](t, request(t, server.Client(), server, http.MethodPost, host, "/oauth/v2/token", strings.NewReader(form.Encode()), formHeader), http.StatusOK)
	}
	browser := newBrowser(t, server)
	done := passwordLogin(t, server, browser, "admin", password)
	callbackURL, err := url.Parse(done.Redirect)
	if err != nil {
		t.Fatal(err)
	}
	callback := request(t, browser, server, http.MethodGet, host, callbackURL.RequestURI(), nil, nil)
	location, err := url.Parse(callback.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := token(url.Values{"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")}, "redirect_uri": {testRedirect}, "client_id": {"bedrock-cli"}, "code_verifier": {"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"}})
	checkTokenAudiences(t, "code", code)
	refresh, _ := code["refresh_token"].(string)
	checkTokenAudiences(t, "refresh", token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"bedrock-cli"}}))
	device := readJSON[map[string]any](t, request(t, server.Client(), server, http.MethodPost, host, "/oauth/v2/device_authorization", strings.NewReader(url.Values{"client_id": {"bedrock-cli"}, "scope": {"openid offline_access"}}.Encode()), formHeader), http.StatusOK)
	userCode, _ := device["user_code"].(string)
	deviceCode, _ := device["device_code"].(string)
	approver := newBrowser(t, server)
	step, _ := postJSON(t, approver, server, "/api/v1/login/device", "", map[string]string{"userCode": userCode})
	step, _ = postJSON(t, approver, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengeUsername, Username: "admin"})
	step, _ = postJSON(t, approver, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengePassword, Password: password})
	approve := true
	step, _ = postJSON(t, approver, server, "/api/v1/login/answer", step.CSRF, methods.Answer{Type: methods.ChallengeDeviceConfirm, Approve: &approve})
	if step.Type != methods.ChallengeDone {
		t.Fatalf("device approval %+v", step)
	}
	checkTokenAudiences(t, "device", token(url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {deviceCode}, "client_id": {"bedrock-cli"}}))
}

func TestConfigCannotCarryACachedReader(t *testing.T) {
	reader := reflect.TypeFor[client.Reader]()
	config := reflect.TypeFor[Config]()
	for index := range config.NumField() {
		if field := config.Field(index); field.Type.Implements(reader) {
			t.Fatalf("Config.%s takes a client.Reader; the server must build its own uncached client", field.Name)
		}
	}
}

func TestCookieKeyIsSharedByReplicas(t *testing.T) {
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	newTestServer(t, cfg, time.Now)
	var first corev1.Secret
	key := client.ObjectKey{Namespace: release.SystemNamespace, Name: "bedrock-authn-cookie-key"}
	if err := c.Get(context.Background(), key, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Data["key"]) != 32 || first.Labels[v1alpha1.LabelAuthn] != "true" || first.Labels[v1alpha1.LabelKind] != "CookieKey" {
		t.Fatalf("cookie key secret %v %d", first.Labels, len(first.Data["key"]))
	}
	newTestServer(t, cfg, time.Now)
	var second corev1.Secret
	if err := c.Get(context.Background(), key, &second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Data["key"], second.Data["key"]) {
		t.Fatal("a second replica must reuse the first writer's key")
	}
	second.Data["key"] = []byte("short")
	if err := c.Update(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), Config{RestConfig: cfg, Random: rand.Reader, Clock: time.Now, UI: fstest.MapFS{}, WebhookSecret: func(context.Context) (string, error) { return testBearer, nil }}); err == nil {
		t.Fatal("a cookie key of the wrong length must stop the server")
	}
}
