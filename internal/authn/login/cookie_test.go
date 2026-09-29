package login

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCookieAttributes(t *testing.T) {
	recorder := httptest.NewRecorder()
	SetCookie(recorder, CookieLogin, "req.value", 10*time.Minute)
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies %v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != "bedrock_login" || cookie.Value != "req.value" || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != 600 {
		t.Fatalf("cookie %+v", cookie)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie flags %+v", cookie)
	}
	cleared := httptest.NewRecorder()
	ClearCookie(cleared, CookieSession)
	gone := cleared.Result().Cookies()[0]
	if gone.Name != "bedrock_session" || gone.Value != "" || gone.MaxAge >= 0 || !gone.HttpOnly || !gone.Secure || gone.Path != "/" {
		t.Fatalf("cleared %+v", gone)
	}
	if CookieUpstream != "bedrock_upstream" {
		t.Fatalf("upstream cookie name %q", CookieUpstream)
	}
}

func TestCSRF(t *testing.T) {
	value, hash, err := NewCSRF(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(value) != 43 || len(hash) != 64 {
		t.Fatalf("value %q hash %q", value, hash)
	}
	cases := []struct {
		hash, header string
		want         bool
	}{
		{hash, value, true},
		{hash, "", false},
		{"", "", false},
		{"", value, false},
		{hash, value + "x", false},
		{hash, hash, false},
	}
	for _, tc := range cases {
		if got := CheckCSRF(tc.hash, tc.header); got != tc.want {
			t.Fatalf("CheckCSRF(%q, %q) = %v, want %v", tc.hash, tc.header, got, tc.want)
		}
	}
	other, _, err := NewCSRF(rand.Reader)
	if err != nil || other == value {
		t.Fatalf("two CSRF values must differ: %q %q %v", value, other, err)
	}
}

func TestClientIP(t *testing.T) {
	forwarded := httptest.NewRequest(http.MethodPost, "/api/v1/login/answer", nil)
	forwarded.Header.Set("X-Forwarded-For", " 203.0.113.7 , 10.0.0.1")
	if got := ClientIP(forwarded); got != "203.0.113.7" {
		t.Fatalf("forwarded %q", got)
	}
	direct := httptest.NewRequest(http.MethodPost, "/api/v1/login/answer", nil)
	direct.RemoteAddr = "192.0.2.4:51234"
	if got := ClientIP(direct); got != "192.0.2.4" {
		t.Fatalf("direct %q", got)
	}
}
