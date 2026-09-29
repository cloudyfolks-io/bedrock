//go:build e2e

package authn

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
)

const (
	loopbackCallback  = "http://127.0.0.1/callback"
	attemptsPerWindow = 8
	attemptWindow     = 65 * time.Second
	requestTimeout    = time.Minute
	maxRedirectHops   = 8
)

type challenge struct {
	Type          string           `json:"type"`
	CSRF          string           `json:"csrf,omitempty"`
	Username      string           `json:"username,omitempty"`
	Methods       []string         `json:"methods,omitempty"`
	Providers     []providerChoice `json:"providers,omitempty"`
	Redirect      string           `json:"redirect,omitempty"`
	Enroll        *totpEnrollment  `json:"enroll,omitempty"`
	RecoveryCodes []string         `json:"recoveryCodes,omitempty"`
	Device        *deviceChallenge `json:"device,omitempty"`
	Error         *challengeError  `json:"error,omitempty"`
}

type providerChoice struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

type totpEnrollment struct {
	OTPAuthURL string `json:"otpauthURL"`
	Secret     string `json:"secret"`
}

type deviceChallenge struct {
	UserCode string   `json:"userCode"`
	ClientID string   `json:"clientID"`
	Scopes   []string `json:"scopes"`
}

type challengeError struct {
	Code string `json:"code"`
}

type loginAnswer struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Code     string `json:"code,omitempty"`
	Provider string `json:"provider,omitempty"`
	Approve  *bool  `json:"approve,omitempty"`
	Method   string `json:"method,omitempty"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

type tokenError struct {
	Error string `json:"error"`
}

type totpState struct {
	seed []byte
	step int64
}

type attemptBudget struct {
	spent []time.Time
}

func recentAttempts(spent []time.Time, now time.Time) []time.Time {
	recent := make([]time.Time, 0, len(spent))
	for _, at := range spent {
		if now.Sub(at) < attemptWindow {
			recent = append(recent, at)
		}
	}
	return recent
}

func waitForRoom(spent []time.Time, now time.Time) time.Duration {
	recent := recentAttempts(spent, now)
	if len(recent) < attemptsPerWindow {
		return 0
	}
	return recent[len(recent)-attemptsPerWindow].Add(attemptWindow).Sub(now)
}

func (b *attemptBudget) await(t *testing.T) {
	t.Helper()
	wait := waitForRoom(b.spent, time.Now())
	if wait <= 0 {
		return
	}
	t.Logf("waiting %s to stay under the login rate limit", wait.Round(time.Second))
	time.Sleep(wait)
}

func (b *attemptBudget) record(at time.Time) {
	b.spent = append(recentAttempts(b.spent, at), at)
}

type loginClient struct {
	http   *http.Client
	base   string
	budget *attemptBudget
}

func newLoginClient(t *testing.T, e env) *loginClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &loginClient{
		base:   e.issuer,
		budget: e.budget,
		http: &http.Client{
			Jar:           jar,
			Timeout:       requestTimeout,
			Transport:     trustingTransport(e.roots),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func trustingTransport(roots *x509.CertPool) *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
}

func certPool(t *testing.T, ca []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatalf("BEDROCK_CA holds no certificate")
	}
	return pool
}

func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func freshTOTP(state totpState) (string, totpState) {
	for methods.TOTPStep(time.Now()) <= state.step {
		time.Sleep(time.Second)
	}
	step := methods.TOTPStep(time.Now())
	return methods.TOTPCode(state.seed, step), totpState{seed: state.seed, step: step}
}

func (c *loginClient) get(t *testing.T, rawURL string) *http.Response {
	t.Helper()
	resp, err := c.http.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	return resp
}

func (c *loginClient) roundTrip(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", req.Method, req.URL.Path, err)
	}
	return resp.StatusCode, data
}

func jsonRequest(t *testing.T, method, rawURL, csrf string, body any) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode %s: %v", rawURL, err)
		}
	}
	req, err := http.NewRequest(method, rawURL, &buf)
	if err != nil {
		t.Fatalf("new request %s: %v", rawURL, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	return req
}

func (c *loginClient) do(t *testing.T, method, path, csrf string, body any) challenge {
	t.Helper()
	status, data := c.roundTrip(t, jsonRequest(t, method, c.base+path, csrf, body))
	if status >= 400 {
		t.Fatalf("%s %s: status %d: %s", method, path, status, data)
	}
	var out challenge
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s: %v: %s", path, err, data)
	}
	return out
}

func authorizeURL(base, clientID, verifier string, scopes []string) string {
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {loopbackCallback},
		"response_type":         {"code"},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {randomToken(16)},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	return base + "/oauth/v2/authorize?" + q.Encode()
}

func (c *loginClient) startAuthorize(t *testing.T, clientID, verifier string, scopes []string) challenge {
	t.Helper()
	resp := c.get(t, authorizeURL(c.base, clientID, verifier, scopes))
	loc := redirectLocation(t, "authorize", resp)
	authRequest := loc.Query().Get("authRequest")
	if authRequest == "" {
		t.Fatalf("authorize: redirect %s carries no authRequest", loc)
	}
	return c.do(t, http.MethodPost, "/api/v1/login/start", "", struct {
		AuthRequest string `json:"authRequest"`
	}{authRequest})
}

func (c *loginClient) startDevice(t *testing.T, userCode string) challenge {
	t.Helper()
	c.budget.await(t)
	c.budget.record(time.Now())
	return c.do(t, http.MethodPost, "/api/v1/login/device", "", struct {
		UserCode string `json:"userCode"`
	}{userCode})
}

func (c *loginClient) answer(t *testing.T, ch challenge, a loginAnswer) challenge {
	t.Helper()
	return c.do(t, http.MethodPost, "/api/v1/login/answer", ch.CSRF, a)
}

func (c *loginClient) attempt(t *testing.T, ch challenge, build func() loginAnswer) challenge {
	t.Helper()
	c.budget.await(t)
	a := build()
	c.budget.record(time.Now())
	return c.answer(t, ch, a)
}

func (c *loginClient) current(t *testing.T) challenge {
	t.Helper()
	return c.do(t, http.MethodGet, "/api/v1/login/challenge", "", nil)
}

func (c *loginClient) username(t *testing.T, ch challenge, username string) challenge {
	t.Helper()
	return c.answer(t, ch, loginAnswer{Type: "username", Username: username})
}

func (c *loginClient) password(t *testing.T, ch challenge, password string) challenge {
	t.Helper()
	return c.attempt(t, ch, func() loginAnswer { return loginAnswer{Type: "password", Password: password} })
}

func (c *loginClient) totpAnswer(t *testing.T, ch challenge, answerType string, state totpState) (challenge, totpState) {
	t.Helper()
	next := state
	out := c.attempt(t, ch, func() loginAnswer {
		code, fresh := freshTOTP(state)
		next = fresh
		return loginAnswer{Type: answerType, Code: code}
	})
	return out, next
}

func (c *loginClient) recovery(t *testing.T, ch challenge, code string) challenge {
	t.Helper()
	return c.attempt(t, ch, func() loginAnswer { return loginAnswer{Type: "recovery", Code: code} })
}

func (c *loginClient) acknowledgeRecoveryCodes(t *testing.T, ch challenge) challenge {
	t.Helper()
	return c.answer(t, ch, loginAnswer{Type: "totp-enroll"})
}

func (c *loginClient) provider(t *testing.T, ch challenge, name string) challenge {
	t.Helper()
	return c.answer(t, ch, loginAnswer{Type: "providers", Provider: name})
}

func (c *loginClient) approveDevice(t *testing.T, ch challenge) challenge {
	t.Helper()
	approve := true
	return c.answer(t, ch, loginAnswer{Type: "device-confirm", Approve: &approve})
}

func (c *loginClient) finishLogin(t *testing.T, ch challenge) string {
	t.Helper()
	if ch.Type != "done" || ch.Redirect == "" {
		t.Fatalf("finishLogin: challenge is %q, want done with a redirect", ch.Type)
	}
	loc := redirectLocation(t, "finishLogin", c.get(t, ch.Redirect))
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("finishLogin: redirect %s carries no code", loc)
	}
	return code
}

func redirectLocation(t *testing.T, step string, resp *http.Response) *url.URL {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode/100 != 3 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: want a redirect from %s, got %d: %s", step, resp.Request.URL, resp.StatusCode, body)
	}
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("%s: no Location: %v", step, err)
	}
	return loc
}

func followUntil(t *testing.T, c *loginClient, resp *http.Response, stop func(*url.URL) bool) *url.URL {
	t.Helper()
	for hop := 0; hop < maxRedirectHops; hop++ {
		loc := redirectLocation(t, "follow redirects", resp)
		if stop(loc) {
			return loc
		}
		resp = c.get(t, loc.String())
	}
	t.Fatalf("follow redirects: no stop within %d redirects", maxRedirectHops)
	return nil
}

func passwordForm(issuerHost string) func(*url.URL) bool {
	return func(loc *url.URL) bool {
		return loc.Host != issuerHost && strings.HasSuffix(loc.Path, "/login")
	}
}

func loginApp(issuerHost string) func(*url.URL) bool {
	return func(loc *url.URL) bool {
		return loc.Host == issuerHost && strings.HasPrefix(loc.Path, "/login/")
	}
}

func (c *loginClient) postUpstreamLogin(t *testing.T, loginURL *url.URL, username, password string) *http.Response {
	t.Helper()
	form := url.Values{"login": {username}, "password": {password}}
	req, err := http.NewRequest(http.MethodPost, loginURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("upstream login request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.budget.await(t)
	c.budget.record(time.Now())
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("upstream login: %v", err)
	}
	return resp
}

func codeForm(clientID, verifier, code string) url.Values {
	return url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {loopbackCallback},
		"code_verifier": {verifier},
	}
}

func refreshForm(clientID, refreshToken string) url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshToken}}
}

func exchangeForm(subjectToken, audience string) url.Values {
	return url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":      {subjectToken},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"audience":           {audience},
	}
}

func tokenRequest(t *testing.T, e env, form url.Values) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.issuer+"/oauth/v2/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new token request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func clientTokenRequest(t *testing.T, e env, form url.Values, clientID, clientSecret string) *http.Request {
	t.Helper()
	req := tokenRequest(t, e, form)
	req.SetBasicAuth(clientID, clientSecret)
	return req
}

func sendToken(t *testing.T, e env, req *http.Request) (int, []byte) {
	t.Helper()
	hc := &http.Client{Timeout: requestTimeout, Transport: trustingTransport(e.roots)}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", req.URL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read token response: %v", err)
	}
	return resp.StatusCode, data
}

func tokensFrom(t *testing.T, e env, req *http.Request) tokenResponse {
	t.Helper()
	status, body := sendToken(t, e, req)
	if status != http.StatusOK {
		t.Fatalf("token: status %d: %s", status, body)
	}
	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("token: decode: %v: %s", err, body)
	}
	return out
}

func tokenRefusal(t *testing.T, e env, req *http.Request) tokenError {
	t.Helper()
	status, body := sendToken(t, e, req)
	if status < 400 {
		t.Fatalf("token: want an error status, got %d: %s", status, body)
	}
	var out tokenError
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("token: decode error body: %v: %s", err, body)
	}
	if out.Error == "" {
		t.Fatalf("token: error body names no error: %s", body)
	}
	return out
}

func jwtPayload(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not a JWT")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		t.Fatalf("unmarshal JWT payload: %v", err)
	}
	return claims
}

func claimStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		texts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				texts = append(texts, text)
			}
		}
		return texts
	}
	return nil
}
