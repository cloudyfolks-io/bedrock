package server

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
)

const (
	credentialGateSlots = 4
	credentialGateWait  = 10 * time.Second
)

type credentialGate struct {
	slots chan struct{}
	wait  time.Duration
}

func newCredentialGate(slots int, wait time.Duration) credentialGate {
	return credentialGate{slots: make(chan struct{}, slots), wait: wait}
}

func (g credentialGate) middleware(matches func(*http.Request) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !matches(r) {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), g.wait)
		defer cancel()
		select {
		case g.slots <- struct{}{}:
		case <-ctx.Done():
			httpjson.WriteError(w, http.StatusTooManyRequests, methods.FailureRateLimited)
			return
		}
		defer func() { <-g.slots }()
		next.ServeHTTP(w, r)
	})
}

func isLoginCredentialCheck(r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == pathLoginAnswer:
		return true
	case r.Method == http.MethodGet && r.URL.Path == pathLoginChallenge:
		return true
	default:
		return false
	}
}

func isAccountCredentialCheck(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case pathAccountPassword, pathAccountTOTPVerify, pathAccountRecoveryCodes:
		return true
	default:
		return false
	}
}
