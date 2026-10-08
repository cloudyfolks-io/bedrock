package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubSessions func(ctx context.Context, cookie string) (Principal, error)

func (s stubSessions) Resolve(ctx context.Context, cookie string) (Principal, error) {
	return s(ctx, cookie)
}

func aliceSessions() SessionSource {
	return stubSessions(func(_ context.Context, cookie string) (Principal, error) {
		switch cookie {
		case "good":
			return Principal{Subject: "alice", Access: "access-alice", CSRF: "csrf-alice"}, nil
		case "ended":
			return Principal{}, ErrSessionEnded
		}
		return Principal{}, ErrUnauthenticated
	})
}

func seenCaller(seen *Caller) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if caller, ok := CallerFrom(r.Context()); ok {
			*seen = caller
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func authRequest(method, target string, header map[string]string, cookie string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	for name, value := range header {
		req.Header.Set(name, value)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieSession, Value: cookie})
	}
	return req
}

func TestCookieAuthUsesTheAccessTokenFromTheRecord(t *testing.T) {
	var seen Caller
	handler := Authenticate(CookieAuth(aliceSessions()), seenCaller(&seen))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authRequest(http.MethodGet, "/", nil, "good"))
	if rec.Code != http.StatusNoContent || seen.Token != "access-alice" || seen.Principal == nil || seen.Principal.Subject != "alice" {
		t.Fatalf("%d %+v", rec.Code, seen)
	}
}

func TestCookieRequestIgnoresAuthorizationHeader(t *testing.T) {
	var seen Caller
	handler := Authenticate(CookieAuth(aliceSessions()), seenCaller(&seen))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authRequest(http.MethodGet, "/", map[string]string{"Authorization": "Bearer attacker.token.value"}, "good"))
	if seen.Token != "access-alice" {
		t.Fatalf("token %q", seen.Token)
	}
}

func TestBearerRequestIgnoresCookies(t *testing.T) {
	var seen Caller
	handler := Authenticate(BearerAuth(), seenCaller(&seen))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authRequest(http.MethodGet, "/", map[string]string{"Authorization": "Bearer aaa.bbb.ccc"}, "good"))
	if rec.Code != http.StatusNoContent || seen.Token != "aaa.bbb.ccc" || seen.Principal != nil {
		t.Fatalf("%d %+v", rec.Code, seen)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, authRequest(http.MethodGet, "/", nil, "good"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cookie only request: %d", rec.Code)
	}
}

func TestBearerShape(t *testing.T) {
	valid := []string{"aaa.bbb.ccc", "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJhIn0.c2ln-_x", "brk_" + strings.Repeat("a", 40)}
	for _, token := range valid {
		if !ValidBearer(token) {
			t.Errorf("ValidBearer(%q) = false", token)
		}
	}
	invalid := []string{"brk_short", "brk_" + strings.Repeat("a", 41), "Basic abc", "", "a b.c.d", "aaa.bbb", "aaa..ccc", "a.b.c.d"}
	for _, token := range invalid {
		if ValidBearer(token) {
			t.Errorf("ValidBearer(%q) = true", token)
		}
	}
}

func TestUnauthenticatedAnswers401Json(t *testing.T) {
	cases := []struct {
		name      string
		auth      Authenticator
		req       *http.Request
		code      string
		challenge string
	}{
		{"no cookie", CookieAuth(aliceSessions()), authRequest(http.MethodGet, "/", nil, ""), "unauthenticated", ""},
		{"unknown cookie", CookieAuth(aliceSessions()), authRequest(http.MethodGet, "/", nil, "bad"), "unauthenticated", ""},
		{"ended session", CookieAuth(aliceSessions()), authRequest(http.MethodGet, "/", nil, "ended"), "session_ended", ""},
		{"bad bearer", BearerAuth(), authRequest(http.MethodGet, "/", map[string]string{"Authorization": "Bearer junk"}, ""), "unauthenticated", "Bearer"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		Authenticate(tc.auth, seenCaller(new(Caller))).ServeHTTP(rec, tc.req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) || rec.Header().Get("WWW-Authenticate") != tc.challenge {
			t.Errorf("%s: %d %s %q", tc.name, rec.Code, rec.Body.String(), rec.Header().Get("WWW-Authenticate"))
		}
	}
}

func chainWithCSRF() http.Handler {
	return Authenticate(CookieAuth(aliceSessions()), RequireCSRF(seenCaller(new(Caller))))
}

func TestUnsafeCookieRequestNeedsCSRF(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for header, want := range map[string]int{"": http.StatusForbidden, "wrong": http.StatusForbidden, "csrf-alice": http.StatusNoContent} {
			headers := map[string]string{}
			if header != "" {
				headers[HeaderCSRF] = header
			}
			rec := httptest.NewRecorder()
			chainWithCSRF().ServeHTTP(rec, authRequest(method, "/", headers, "good"))
			if rec.Code != want || want == http.StatusForbidden && !strings.Contains(rec.Body.String(), `"csrf"`) {
				t.Errorf("%s with %q: %d %s", method, header, rec.Code, rec.Body.String())
			}
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		rec := httptest.NewRecorder()
		chainWithCSRF().ServeHTTP(rec, authRequest(method, "/", nil, "good"))
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s: %d", method, rec.Code)
		}
	}
	bearer := Authenticate(BearerAuth(), RequireCSRF(seenCaller(new(Caller))))
	rec := httptest.NewRecorder()
	bearer.ServeHTTP(rec, authRequest(http.MethodPost, "/", map[string]string{"Authorization": "Bearer aaa.bbb.ccc"}, ""))
	if rec.Code != http.StatusNoContent {
		t.Errorf("bearer POST: %d", rec.Code)
	}
}

func TestCrossSiteFetchRefused(t *testing.T) {
	handler := RefuseCrossSite(seenCaller(new(Caller)))
	cases := []struct {
		method string
		site   string
		want   int
	}{
		{http.MethodPost, "cross-site", http.StatusForbidden},
		{http.MethodDelete, "cross-site", http.StatusForbidden},
		{http.MethodPost, "same-origin", http.StatusNoContent},
		{http.MethodPost, "same-site", http.StatusNoContent},
		{http.MethodPost, "", http.StatusNoContent},
		{http.MethodGet, "cross-site", http.StatusNoContent},
		{http.MethodHead, "cross-site", http.StatusNoContent},
	}
	for _, tc := range cases {
		headers := map[string]string{}
		if tc.site != "" {
			headers["Sec-Fetch-Site"] = tc.site
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, authRequest(tc.method, "/", headers, "good"))
		if rec.Code != tc.want {
			t.Errorf("%s %q: %d", tc.method, tc.site, rec.Code)
		}
	}
}

func TestOriginMustMatchConsoleHost(t *testing.T) {
	chain := Authenticate(CookieAuth(aliceSessions()), RequireOrigin(labHost, seenCaller(new(Caller))))
	upgrade := map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"}
	with := func(origin string, base map[string]string) map[string]string {
		headers := map[string]string{}
		for name, value := range base {
			headers[name] = value
		}
		if origin != "" {
			headers["Origin"] = origin
		}
		return headers
	}
	status := func(method string, headers map[string]string) int {
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, authRequest(method, "/", headers, "good"))
		return rec.Code
	}
	if got := status(http.MethodGet, with("https://console.lab.example", upgrade)); got != http.StatusNoContent {
		t.Errorf("good origin upgrade: %d", got)
	}
	if got := status(http.MethodPost, with("https://console.lab.example", nil)); got != http.StatusNoContent {
		t.Errorf("good origin POST: %d", got)
	}
	bad := []string{"https://console.lab.example.evil.test", "http://console.lab.example", "https://console.lab.example:8443", "null", "https://lab.example"}
	for _, origin := range bad {
		if got := status(http.MethodGet, with(origin, upgrade)); got != http.StatusForbidden {
			t.Errorf("origin %q upgrade: %d", origin, got)
		}
	}
	if got := status(http.MethodGet, with("", upgrade)); got != http.StatusForbidden {
		t.Errorf("missing origin upgrade: %d", got)
	}
	if got := status(http.MethodPost, with("", nil)); got != http.StatusForbidden {
		t.Errorf("missing origin POST: %d", got)
	}
	if got := status(http.MethodGet, with("", nil)); got != http.StatusNoContent {
		t.Errorf("missing origin safe GET: %d", got)
	}
}

func TestWebSocketUpgradeIsDetected(t *testing.T) {
	yes := authRequest(http.MethodGet, "/", map[string]string{"Connection": "keep-alive, Upgrade", "Upgrade": "WebSocket"}, "")
	if !isWebSocketUpgrade(yes) {
		t.Error("upgrade with a connection list was not detected")
	}
	no := authRequest(http.MethodGet, "/", map[string]string{"Connection": "keep-alive", "Upgrade": "websocket"}, "")
	if isWebSocketUpgrade(no) {
		t.Error("request without the upgrade token counts as an upgrade")
	}
}
