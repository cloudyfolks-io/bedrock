package proxy

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
)

type sessionUser struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Email       string   `json:"email"`
	Groups      []string `json:"groups"`
}

type sessionView struct {
	User      sessionUser `json:"user"`
	Admin     bool        `json:"admin"`
	CSRF      string      `json:"csrf"`
	ExpiresAt string      `json:"expiresAt"`
	Host      string      `json:"host"`
}

func SessionHandler(host func(context.Context) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, found := CallerFrom(r.Context())
		if !found || caller.Principal == nil {
			httpjson.WriteError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		current, err := host(r.Context())
		if err != nil {
			hostUnavailable(w, r, err)
			return
		}
		principal := caller.Principal
		httpjson.Write(w, http.StatusOK, sessionView{
			User: sessionUser{
				Name:        principal.Subject,
				DisplayName: principal.DisplayName,
				Email:       principal.Email,
				Groups:      append([]string{}, principal.Groups...),
			},
			Admin:     principal.Admin,
			CSRF:      principal.CSRF,
			ExpiresAt: principal.ExpiresAt.UTC().Format(time.RFC3339),
			Host:      current,
		})
	})
}
