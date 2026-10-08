package proxy

import (
	"net/http"
	"time"
)

const loginCookieLife = 10 * time.Minute

func SetSessionCookie(w http.ResponseWriter, value string, maxAge time.Duration) {
	http.SetCookie(w, hostCookie(CookieSession, value, int(maxAge.Seconds())))
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, hostCookie(CookieSession, "", -1))
}

func setLoginCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, hostCookie(CookieLogin, state, int(loginCookieLife.Seconds())))
}

func clearLoginCookie(w http.ResponseWriter) {
	http.SetCookie(w, hostCookie(CookieLogin, "", -1))
}

func hostCookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}
