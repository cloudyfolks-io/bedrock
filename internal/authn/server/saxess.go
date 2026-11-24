package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/saxess"
)

func saxessLoginConfig(ctx context.Context, backend *saxess.Backend, host func(context.Context) (string, error)) (*login.SAxessConfig, error) {
	if backend == nil {
		return nil, nil
	}
	base, err := host(ctx)
	if err != nil {
		return nil, err
	}
	if base == "" {
		return nil, errors.New("sAxess requires a configured console host")
	}
	return &login.SAxessConfig{
		Backend: backend, ClientID: "bedrock-console", RedirectURI: "https://console." + base + "/oauth/callback",
	}, nil
}

func mountSAxessMobile(mux *http.ServeMux, backend *saxess.Backend, clock func() time.Time) {
	if backend == nil {
		return
	}
	mobile := saxess.NewMobileHTTP(backend.Authenticate, backend)
	enrollment := saxess.NewRegistrationHTTP(backend)
	limiter := methods.NewRateLimiter(startsPerMinute, time.Minute)
	mount := func(path string, handle http.HandlerFunc) {
		mux.Handle(path, limitByClientIP(limiter, clock, handle))
	}
	mount("/api/employees/devices/register", enrollment.RegisterDevice)
	mount("/api/employees/get-details", backend.GetDetails)
	mount("/api/employees/get-requests", mobile.GetRequests)
	mount("/api/auth/send_consent", mobile.SendConsent)
}
