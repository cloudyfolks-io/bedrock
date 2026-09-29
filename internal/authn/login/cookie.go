package login

import (
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	CookieLogin    = "bedrock_login"
	CookieSession  = "bedrock_session"
	CookieUpstream = "bedrock_upstream"
)

func SetCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func ClearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func ClientIP(r *http.Request) string {
	if hop := lastForwardedHop(r.Header.Values("X-Forwarded-For")); hop != "" {
		return hop
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func lastForwardedHop(values []string) string {
	if len(values) == 0 {
		return ""
	}
	hops := strings.Split(values[len(values)-1], ",")
	return strings.TrimSpace(hops[len(hops)-1])
}
