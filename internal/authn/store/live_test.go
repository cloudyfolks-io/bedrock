package store

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type movingClock struct{ now time.Time }

func (m *movingClock) Now() time.Time { return m.now }

func cappedStore(c client.Client, clock *movingClock, auth, device int) *Store {
	s := New(Config{
		Client:   c,
		Reader:   c,
		Random:   rand.Reader,
		Clock:    clock.Now,
		Settings: func(context.Context) (policy.Settings, error) { return testSettings(), nil },
		Keys:     func(context.Context) ([]keys.Key, error) { return nil, nil },
	})
	s.created = createdRequests{
		auth:   methods.NewRateLimiter(auth, authRequestLifetime),
		device: methods.NewRateLimiter(device, deviceLifetime),
	}
	return s
}

func isTemporarilyUnavailable(err error) bool {
	var refused *oidc.Error
	return errors.As(err, &refused) && refused.ErrorType == "temporarily_unavailable"
}

func TestStoreCapsLiveRequestsByDefault(t *testing.T) {
	s := New(Config{})
	if !s.created.auth.Allow(liveKey, testNow) || !s.created.device.Allow(liveKey, testNow) {
		t.Fatal("a new store must accept requests")
	}
	if liveAuthRequests != 5000 || liveDeviceRequests != 1000 {
		t.Fatalf("caps %d %d", liveAuthRequests, liveDeviceRequests)
	}
}

func TestLiveAuthRequestsAreCapped(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, publicClient("bedrock-cli"))
	clock := &movingClock{now: testNow}
	s := cappedStore(c, clock, 2, 2)
	evil := authorizeRequest("bedrock-cli")
	evil.RedirectURI = "https://evil.test/callback"
	if _, err := s.CreateAuthRequest(ctx, evil, ""); err == nil || isTemporarilyUnavailable(err) {
		t.Fatalf("an invalid request is refused for itself: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := s.CreateAuthRequest(ctx, authorizeRequest("bedrock-cli"), ""); err != nil {
			t.Fatalf("request %d: %v", attempt, err)
		}
	}
	if _, err := s.CreateAuthRequest(ctx, authorizeRequest("bedrock-cli"), ""); !isTemporarilyUnavailable(err) {
		t.Fatalf("a request over the cap: %v", err)
	}
	var stored v1alpha1.AuthRequestList
	if err := c.List(ctx, &stored, client.InNamespace(release.SystemNamespace)); err != nil || len(stored.Items) != 2 {
		t.Fatalf("stored %d requests, err %v", len(stored.Items), err)
	}
	clock.now = clock.now.Add(authRequestLifetime + time.Second)
	if _, err := s.CreateAuthRequest(ctx, authorizeRequest("bedrock-cli"), ""); err != nil {
		t.Fatalf("after the lifetime the cap frees up: %v", err)
	}
}

func TestLiveDeviceRequestsAreCapped(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	clock := &movingClock{now: testNow}
	s := cappedStore(c, clock, 2, 2)
	expires := testNow.Add(deviceLifetime)
	for index, userCode := range []string{"BCDF-GHJK", "LMNP-QRST"} {
		if err := s.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-"+userCode, userCode, expires, []string{"openid"}); err != nil {
			t.Fatalf("device request %d: %v", index+1, err)
		}
	}
	if err := s.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-over", "VWXZ-BCDF", expires, []string{"openid"}); !isTemporarilyUnavailable(err) {
		t.Fatalf("a device request over the cap: %v", err)
	}
	var stored v1alpha1.DeviceRequestList
	if err := c.List(ctx, &stored, client.InNamespace(release.SystemNamespace)); err != nil || len(stored.Items) != 2 {
		t.Fatalf("stored %d device requests, err %v", len(stored.Items), err)
	}
	clock.now = clock.now.Add(deviceLifetime + time.Second)
	if err := s.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-later", "VWXZ-BCDF", expires.Add(deviceLifetime), []string{"openid"}); err != nil {
		t.Fatalf("after the lifetime the cap frees up: %v", err)
	}
}
