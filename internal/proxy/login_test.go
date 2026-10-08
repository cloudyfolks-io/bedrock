package proxy

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

var sessionCounter atomic.Int32

type loginFixture struct {
	fake     *fakeAuthn
	store    *Store
	start    http.Handler
	callback http.Handler
	logout   http.Handler
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	c := startEnv(t)
	fake := newFakeAuthn(t)
	host := &mutableHost{value: "lab.example"}
	store := NewStore(c, time.Now)
	start, callback, logout := LoginHandlers(LoginDeps{
		Store:     store,
		OIDC:      newOIDCFor(fake, host, fakeClientSecret),
		Reader:    c,
		Random:    rand.Reader,
		Clock:     time.Now,
		Limiter:   methods.NewRateLimiter(30, time.Minute),
		Callbacks: methods.NewRateLimiter(30, time.Minute),
		Evict:     func(string) {},
		Signer:    NewCookieSigner([]byte("0123456789abcdef0123456789abcdef")),
		Host:      host.Get,
	})
	return &loginFixture{fake: fake, store: store, start: start, callback: callback, logout: logout}
}

func newAuthnSession(t *testing.T, fake *fakeAuthn, life time.Duration) string {
	t.Helper()
	name := "sid-login-" + strconv.Itoa(int(sessionCounter.Add(1)))
	session := &v1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "bedrock-system"},
		Spec: v1alpha1.SessionSpec{
			UserRef:   "alice",
			AuthTime:  metav1.Now(),
			ExpiresAt: metav1.NewTime(time.Now().Add(life)),
		},
	}
	if err := testClient.Create(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	fake.mutate(func(f *fakeAuthn) { f.identity.Session = name })
	return name
}

func (fx *loginFixture) begin(t *testing.T, target string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	fx.start.ServeHTTP(rec, req)
	return rec, cookieNamed(rec.Result(), CookieLogin)
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, cookie := range resp.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func (fx *loginFixture) finish(t *testing.T, location string, loginCookie *http.Cookie, state string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/oauth/callback?code=" + url.QueryEscape(fx.fake.CodeFor(location)) + "&state=" + url.QueryEscape(state)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.AddCookie(loginCookie)
	rec := httptest.NewRecorder()
	fx.callback.ServeHTTP(rec, req)
	return rec
}

func stateOf(t *testing.T, location string) string {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get("state")
}

func TestLoginStartRedirectsToAuthn(t *testing.T) {
	fx := newLoginFixture(t)
	rec, cookie := fx.begin(t, "/oauth/login?returnTo=/p/web/vms%3Fx%3D1")
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d", rec.Code)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "https://sso.lab.example/oauth/v2/authorize?") {
		t.Fatalf("Location %q", location)
	}
	state := stateOf(t, location)
	if cookie == nil || cookie.Value != state || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != 600 {
		t.Fatalf("login cookie %+v", cookie)
	}
	var stored corev1.Secret
	if err := testClient.Get(context.Background(), client.ObjectKey{Namespace: StoreNamespace, Name: "pl-" + secret.SHA256Hex(state)}, &stored); err != nil {
		t.Fatal(err)
	}
	pending, err := fx.store.TakeLogin(context.Background(), secret.SHA256Hex(state))
	if err != nil || pending.Verifier == "" || pending.Nonce == "" || pending.ReturnTo != "/p/web/vms?x=1" {
		t.Fatalf("login record %+v, %v", pending, err)
	}
}

func TestSafeReturnTo(t *testing.T) {
	keep := []string{"/p/web/vms?x=1", "/"}
	for _, value := range keep {
		if got := SafeReturnTo(value); got != value {
			t.Errorf("SafeReturnTo(%q) = %q", value, got)
		}
	}
	refuse := []string{
		"", "//evil.test", `/\evil.test`, "https://evil.test", "javascript:alert(1)", "/a\nb", "/a%0d%0ab",
		"/" + strings.Repeat("a", 512), "evil", "/%2f/evil.test", "/%5cevil.test", "/%zz",
	}
	for _, value := range refuse {
		if got := SafeReturnTo(value); got != "/" {
			t.Errorf("SafeReturnTo(%q) = %q, want /", value, got)
		}
	}
}

func TestCallbackCreatesSession(t *testing.T) {
	fx := newLoginFixture(t)
	sid := newAuthnSession(t, fx.fake, 12*time.Hour)
	rec, cookie := fx.begin(t, "/oauth/login?returnTo=/p/web/vms")
	state := stateOf(t, rec.Header().Get("Location"))
	done := fx.finish(t, rec.Header().Get("Location"), cookie, state)
	if done.Code != http.StatusFound || done.Header().Get("Location") != "/p/web/vms" {
		t.Fatalf("callback %d %q", done.Code, done.Header().Get("Location"))
	}
	session := cookieNamed(done.Result(), CookieSession)
	if session == nil || len(session.Value) != 43+1+64 || !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode ||
		session.Path != "/" || session.Domain != "" || session.MaxAge < int((12*time.Hour-time.Minute).Seconds()) || session.MaxAge > int((12*time.Hour).Seconds()) {
		t.Fatalf("session cookie %+v", session)
	}
	stored, err := fx.store.Session(context.Background(), secret.SHA256Hex(session.Value))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Subject != "alice" || strings.Join(stored.Groups, ",") != "ops" || stored.SessionName != sid ||
		stored.Access == "" || stored.Refresh == "" || len(stored.CSRF) != 43 {
		t.Fatalf("record %+v", stored)
	}
	if _, err := fx.store.TakeLogin(context.Background(), secret.SHA256Hex(state)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("login record survived: %v", err)
	}
}

func TestCallbackRejectsStateMismatch(t *testing.T) {
	fx := newLoginFixture(t)
	newAuthnSession(t, fx.fake, time.Hour)
	rec, cookie := fx.begin(t, "/oauth/login")
	done := fx.finish(t, rec.Header().Get("Location"), cookie, "another-state")
	if done.Code != http.StatusBadRequest {
		t.Fatalf("status %d", done.Code)
	}
}

func TestCallbackWithoutLoginCookie(t *testing.T) {
	fx := newLoginFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=c&state=s", nil)
	rec := httptest.NewRecorder()
	fx.callback.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestCallbackIsSingleUse(t *testing.T) {
	fx := newLoginFixture(t)
	newAuthnSession(t, fx.fake, time.Hour)
	rec, cookie := fx.begin(t, "/oauth/login")
	location := rec.Header().Get("Location")
	state := stateOf(t, location)
	if first := fx.finish(t, location, cookie, state); first.Code != http.StatusFound {
		t.Fatalf("first callback %d", first.Code)
	}
	before := fx.fake.Calls("token:authorization_code")
	if second := fx.finish(t, location, cookie, state); second.Code != http.StatusBadRequest || cookieNamed(second.Result(), CookieSession) != nil {
		t.Fatalf("second callback %d", second.Code)
	}
	if fx.fake.Calls("token:authorization_code") != before {
		t.Fatal("second callback reached the token endpoint")
	}
}

func TestCallbackAuthnError(t *testing.T) {
	fx := newLoginFixture(t)
	target := "/oauth/callback?error=access_denied&error_description=" + url.QueryEscape("<script>x</script>")
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	fx.callback.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusForbidden || !strings.Contains(body, `href="/oauth/login"`) ||
		strings.Contains(body, "<script>") || strings.Contains(body, "access_denied") {
		t.Fatalf("%d %s", rec.Code, body)
	}
}

func TestCallbackFailsWithoutAuthnSession(t *testing.T) {
	fx := newLoginFixture(t)
	fx.fake.mutate(func(f *fakeAuthn) { f.identity.Session = "sid-does-not-exist" })
	rec, cookie := fx.begin(t, "/oauth/login")
	done := fx.finish(t, rec.Header().Get("Location"), cookie, stateOf(t, rec.Header().Get("Location")))
	if done.Code != http.StatusBadGateway || cookieNamed(done.Result(), CookieSession) != nil {
		t.Fatalf("status %d", done.Code)
	}
}

func TestLoginStartIsRateLimited(t *testing.T) {
	fx := newLoginFixture(t)
	send := func(remote, forwarded string) int {
		req := httptest.NewRequest(http.MethodGet, "/oauth/login", nil)
		req.RemoteAddr = remote
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
		}
		rec := httptest.NewRecorder()
		fx.start.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 30; i++ {
		if code := send("192.0.2.1:1000", "6.6.6."+strconv.Itoa(i)+", 10.0.0.9"); code != http.StatusFound {
			t.Fatalf("start %d answered %d", i, code)
		}
	}
	if code := send("192.0.2.1:1000", "6.6.6.6, 10.0.0.9"); code != http.StatusTooManyRequests {
		t.Fatalf("31st start answered %d", code)
	}
	if code := send("192.0.2.1:1000", "6.6.6.6, 10.0.0.10"); code != http.StatusFound {
		t.Fatalf("other client answered %d", code)
	}
}

func TestLogoutRevokesAndClears(t *testing.T) {
	fx := newLoginFixture(t)
	newAuthnSession(t, fx.fake, time.Hour)
	rec, cookie := fx.begin(t, "/oauth/login")
	location := rec.Header().Get("Location")
	done := fx.finish(t, location, cookie, stateOf(t, location))
	session := cookieNamed(done.Result(), CookieSession)
	hash := secret.SHA256Hex(session.Value)
	stored, err := fx.store.Session(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/logout", nil)
	req.AddCookie(session)
	out := httptest.NewRecorder()
	fx.logout.ServeHTTP(out, req)
	if out.Code != http.StatusNoContent {
		t.Fatalf("status %d", out.Code)
	}
	cleared := cookieNamed(out.Result(), CookieSession)
	if cleared == nil || cleared.MaxAge != -1 {
		t.Fatalf("cleared cookie %+v", cleared)
	}
	revoked := fx.fake.Revoked()
	if len(revoked) != 1 || revoked[0] != stored.Refresh {
		t.Fatalf("revoked %v", revoked)
	}
	if _, err := fx.store.Session(context.Background(), hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("record survived: %v", err)
	}
}

func TestLogoutWithoutSessionIsNoContent(t *testing.T) {
	fx := newLoginFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/oauth/logout", nil)
	out := httptest.NewRecorder()
	fx.logout.ServeHTTP(out, req)
	if out.Code != http.StatusNoContent {
		t.Fatalf("status %d", out.Code)
	}
}

func TestCallbackIsRateLimited(t *testing.T) {
	fx := newLoginFixture(t)
	for i := 0; i < 30; i++ {
		req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=c&state=s", nil)
		rec := httptest.NewRecorder()
		fx.callback.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("callback %d answered %d", i, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=c&state=s", nil)
	rec := httptest.NewRecorder()
	fx.callback.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("31st callback answered %d", rec.Code)
	}
	other := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=c&state=s", nil)
	other.RemoteAddr = "192.0.2.77:1000"
	rec = httptest.NewRecorder()
	fx.callback.ServeHTTP(rec, other)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("other client answered %d", rec.Code)
	}
}
