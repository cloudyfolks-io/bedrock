package server

import (
	"net/http"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
)

func limitByClientIP(limiter *methods.RateLimiter, clock func() time.Time, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(login.ClientIP(r), clock()) {
			httpjson.WriteError(w, http.StatusTooManyRequests, methods.FailureRateLimited)
			return
		}
		next.ServeHTTP(w, r)
	})
}
