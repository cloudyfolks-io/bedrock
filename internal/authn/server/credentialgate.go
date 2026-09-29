package server

import (
	"context"
	"net/http"
	"time"

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
			writeError(w, http.StatusTooManyRequests, methods.FailureRateLimited)
			return
		}
		defer func() { <-g.slots }()
		next.ServeHTTP(w, r)
	})
}

func isLoginAnswer(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == pathLoginAnswer
}

func isAccountCredentialCheck(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == pathAccountPassword || r.URL.Path == pathAccountTOTPVerify)
}
