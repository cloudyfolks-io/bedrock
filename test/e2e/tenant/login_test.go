//go:build e2e

package tenant

import (
	"bytes"
	"encoding/base32"
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

type challenge struct {
	Type     string          `json:"type"`
	CSRF     string          `json:"csrf,omitempty"`
	Redirect string          `json:"redirect,omitempty"`
	Enroll   *totpEnrollment `json:"enroll,omitempty"`
}

type totpEnrollment struct {
	Secret string `json:"secret"`
}

type loginAnswer struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Code     string `json:"code,omitempty"`
}

type totpState struct {
	seed []byte
	step int64
}

type loginClient struct {
	http *http.Client
	jar  http.CookieJar
	sso  string
}

func newLoginClient(t *testing.T, e env) *loginClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &loginClient{
		jar: jar,
		sso: "https://sso." + e.host,
		http: &http.Client{
			Jar:           jar,
			Timeout:       requestTimeout,
			Transport:     trustingTransport(e.roots),
			CheckRedirect: noRedirects,
		},
	}
}

func decodeSeed(t *testing.T, encoded string) []byte {
	t.Helper()
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(encoded))
	if err != nil {
		t.Fatalf("decode totp secret: %v", err)
	}
	return seed
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

func redirectLocation(t *testing.T, step string, resp *http.Response) *url.URL {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode/100 != 3 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: want a redirect from %s, got %d: %s", step, resp.Request.URL, resp.StatusCode, printable(body))
	}
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("%s: no Location: %v", step, err)
	}
	return loc
}

func (c *loginClient) call(t *testing.T, method, path, csrf string, body any) challenge {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode %s: %v", path, err)
		}
	}
	req, err := http.NewRequest(method, c.sso+path, &buf)
	if err != nil {
		t.Fatalf("new request %s: %v", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("%s %s: status %d: %s", method, path, resp.StatusCode, printable(data))
	}
	var out challenge
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s: %v: %s", path, err, printable(data))
	}
	return out
}

func (c *loginClient) startAt(t *testing.T, authorizeURL string) challenge {
	t.Helper()
	loc := redirectLocation(t, "authorize", c.get(t, authorizeURL))
	authRequest := loc.Query().Get("authRequest")
	if authRequest == "" {
		t.Fatalf("authorize: redirect %s carries no authRequest", loc)
	}
	return c.call(t, http.MethodPost, "/api/v1/login/start", "", struct {
		AuthRequest string `json:"authRequest"`
	}{authRequest})
}

func (c *loginClient) answer(t *testing.T, ch challenge, a loginAnswer) challenge {
	t.Helper()
	return c.call(t, http.MethodPost, "/api/v1/login/answer", ch.CSRF, a)
}

func requireChallenge(t *testing.T, step string, ch challenge, want string) {
	t.Helper()
	if ch.Type != want {
		t.Fatalf("%s: challenge is %q, want %q", step, ch.Type, want)
	}
}

func (c *loginClient) signInAsAdmin(t *testing.T, e env, password string) {
	t.Helper()
	start := c.get(t, e.consoleURL("/oauth/login?returnTo=/admin/hosts"))
	authorize := redirectLocation(t, "console login", start)
	if authorize.Host != "sso."+e.host {
		t.Fatalf("console login redirects to %s, want sso.%s", authorize, e.host)
	}
	ch := c.startAt(t, authorize.String())
	requireChallenge(t, "start", ch, "username")
	ch = c.answer(t, ch, loginAnswer{Type: "username", Username: "admin"})
	requireChallenge(t, "after username", ch, "password")
	ch = c.answer(t, ch, loginAnswer{Type: "password", Password: password})
	requireChallenge(t, "after password", ch, "totp-enroll")
	if ch.Enroll == nil || ch.Enroll.Secret == "" {
		t.Fatalf("the totp-enroll challenge carries no secret")
	}
	code, _ := freshTOTP(totpState{seed: decodeSeed(t, ch.Enroll.Secret)})
	ch = c.answer(t, ch, loginAnswer{Type: "totp-enroll", Code: code})
	requireChallenge(t, "after the enrollment code", ch, "totp-enroll")
	ch = c.answer(t, ch, loginAnswer{Type: "totp-enroll"})
	requireChallenge(t, "after the recovery codes", ch, "done")
	callback := redirectLocation(t, "authorize callback", c.get(t, ch.Redirect))
	if callback.Host != "console."+e.host || callback.Path != "/oauth/callback" {
		t.Fatalf("authorize callback redirects to %s, want the console callback", callback)
	}
	landing := redirectLocation(t, "console callback", c.get(t, callback.String()))
	if landing.Path != "/admin/hosts" {
		t.Fatalf("console callback redirects to %s, want /admin/hosts", landing)
	}
}
