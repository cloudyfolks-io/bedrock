package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sessionRequest(cookie string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, PathSession, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieSession, Value: cookie})
	}
	return req
}

func TestSessionEndpointShape(t *testing.T) {
	expires := time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)
	sessions := stubSessions(func(_ context.Context, cookie string) (Principal, error) {
		groups := []string{"ops"}
		if cookie == "admin" {
			groups = []string{"bedrock-admins"}
		}
		return Principal{
			Subject: "admin", DisplayName: "Administrator", Email: "admin@example.test", Groups: groups,
			Admin: cookie == "admin", Access: "secret-access", CSRF: strings.Repeat("c", 43), ExpiresAt: expires,
		}, nil
	})
	handler := Authenticate(CookieAuth(sessions), SessionHandler(func(context.Context) (string, error) { return "e2e.bedrock.test", nil }))
	rec := do(handler, sessionRequest("admin"))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	user := got["user"].(map[string]any)
	if user["name"] != "admin" || user["displayName"] != "Administrator" || user["email"] != "admin@example.test" ||
		got["admin"] != true || got["host"] != "e2e.bedrock.test" || got["expiresAt"] != "2026-10-09T01:02:03Z" || len(got["csrf"].(string)) != 43 {
		t.Fatalf("body %s", rec.Body.String())
	}
	for _, leaked := range []string{"secret-access", "access", "refresh", "sessionName"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Errorf("body holds %q", leaked)
		}
	}
	plain := do(handler, sessionRequest("ops"))
	if !strings.Contains(plain.Body.String(), `"admin":false`) {
		t.Fatalf("non-admin body %s", plain.Body.String())
	}
}

func TestSessionEndpointWithoutCookie(t *testing.T) {
	handler := Authenticate(CookieAuth(aliceSessions()), SessionHandler(labHost))
	rec := do(handler, sessionRequest(""))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"unauthenticated"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionEndpointListsNoGroupsAsEmptyArray(t *testing.T) {
	sessions := stubSessions(func(context.Context, string) (Principal, error) { return Principal{Subject: "bob"}, nil })
	rec := do(Authenticate(CookieAuth(sessions), SessionHandler(labHost)), sessionRequest("any"))
	if !strings.Contains(rec.Body.String(), `"groups":[]`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}
