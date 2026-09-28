package store

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

func TestUserCodeNormalizes(t *testing.T) {
	for _, input := range []string{"ABCD-EFGH", "abcd-efgh", "abcd efgh", " AbCd-EfGh ", "ABCDEFGH"} {
		if got := NormalizeUserCode(input); got != "ABCDEFGH" {
			t.Fatalf("NormalizeUserCode(%q) = %q, want ABCDEFGH", input, got)
		}
	}
}

func TestDeviceConfig(t *testing.T) {
	cfg := DeviceConfig()
	if cfg.Lifetime != 10*time.Minute || cfg.PollInterval != 5*time.Second || cfg.UserFormPath != "/login/device" || cfg.UserFormURL != "" {
		t.Fatalf("device config %+v", cfg)
	}
	if cfg.UserCode != (op.UserCodeConfig{CharSet: UserCodeAlphabet, CharAmount: 8, DashInterval: 4}) {
		t.Fatalf("user code config %+v", cfg.UserCode)
	}
	if len(UserCodeAlphabet) != 22 || strings.ContainsAny(UserCodeAlphabet, "IOUY") {
		t.Fatalf("alphabet %q must have 22 letters without I, O, U, Y", UserCodeAlphabet)
	}
	pattern := regexp.MustCompile("^[" + UserCodeAlphabet + "]{4}-[" + UserCodeAlphabet + "]{4}$")
	for range 20 {
		code, err := op.NewUserCode([]rune(cfg.UserCode.CharSet), cfg.UserCode.CharAmount, cfg.UserCode.DashInterval)
		if err != nil || !pattern.MatchString(code) || len(NormalizeUserCode(code)) != 8 {
			t.Fatalf("user code %q, err %v", code, err)
		}
	}
}

func TestDeviceFlowStates(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	expires := testNow.Add(10 * time.Minute)
	scopes := []string{"openid", "offline_access"}
	if err := s.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-1", "WXZB-CDFG", expires, scopes); err != nil {
		t.Fatal(err)
	}
	var stored v1alpha1.DeviceRequest
	if err := c.Get(ctx, objectKey(secret.SHA256Hex("device-code-1")), &stored); err != nil {
		t.Fatalf("the request must be stored under the device code's SHA-256: %v", err)
	}
	if stored.Spec.UserCodeHash != secret.SHA256Hex("WXZBCDFG") || stored.Status.State != v1alpha1.DeviceStatePending || !reflect.DeepEqual(stored.Spec.Audience, []string{"bedrock", "bedrock-cli"}) || !stored.Spec.ExpiresAt.Time.Equal(expires) {
		t.Fatalf("stored request %+v %+v", stored.Spec, stored.Status)
	}
	if err := s.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-2", "wxzb cdfg", expires, scopes); !errors.Is(err, op.ErrDuplicateUserCode) {
		t.Fatalf("a live user code must not be issued twice, got %v", err)
	}

	pending, err := s.GetDeviceAuthorizatonState(ctx, "bedrock-cli", "device-code-1")
	if err != nil || pending.Done || pending.Denied || !pending.Expires.Equal(expires) {
		t.Fatalf("pending state %+v, err %v", pending, err)
	}
	if err := c.Get(ctx, objectKey(stored.Name), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.LastPoll == nil || !stored.Status.LastPoll.Time.Equal(testNow) {
		t.Fatalf("last poll %v, want %v", stored.Status.LastPoll, testNow)
	}
	if _, err := s.GetDeviceAuthorizatonState(ctx, "other-client", "device-code-1"); !isNotFound(err) {
		t.Fatalf("another client must not see the request, got %v", err)
	}

	found, err := s.DeviceRequestByUserCode(ctx, "wxzb-cdfg")
	if err != nil || found.Name != stored.Name {
		t.Fatalf("by user code %q, err %v", found.Name, err)
	}
	if err := s.ApproveDevice(ctx, found.Name, methods.Subject{User: *testUser("alice"), AMR: []string{"pwd", "otp"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DenyDevice(ctx, found.Name); !errors.Is(err, errDeviceDecided) {
		t.Fatalf("a decided request must not change, got %v", err)
	}
	if _, err := s.DeviceRequestByUserCode(ctx, "WXZB-CDFG"); !isNotFound(err) {
		t.Fatalf("an approved request is no longer pending, got %v", err)
	}
	approved, err := s.GetDeviceAuthorizatonState(ctx, "bedrock-cli", "device-code-1")
	if err != nil {
		t.Fatal(err)
	}
	if !approved.Done || approved.Subject != "alice" || !reflect.DeepEqual(approved.AMR, []string{"pwd", "otp"}) || !approved.AuthTime.Equal(testNow) || !reflect.DeepEqual(approved.GetAudience(), []string{"bedrock", "bedrock-cli"}) || !reflect.DeepEqual(approved.Scopes, scopes) {
		t.Fatalf("approved state %+v", approved)
	}
	if _, err := s.GetDeviceAuthorizatonState(ctx, "bedrock-cli", "device-code-1"); err == nil {
		t.Fatal("a device code must yield tokens once")
	}

	if err := s.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-3", "BCDF-GHJK", expires, scopes); err != nil {
		t.Fatal(err)
	}
	denied, err := s.DeviceRequestByUserCode(ctx, "BCDFGHJK")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DenyDevice(ctx, denied.Name); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		state, err := s.GetDeviceAuthorizatonState(ctx, "bedrock-cli", "device-code-3")
		if err != nil || !state.Denied || state.Done {
			t.Fatalf("denied state %+v, err %v", state, err)
		}
	}
}

func TestDeviceDecisionRejectsStaleRead(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	expires := testNow.Add(10 * time.Minute)
	if err := s.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-race", "PQRS-TVWX", expires, []string{"openid"}); err != nil {
		t.Fatal(err)
	}
	found, err := s.DeviceRequestByUserCode(ctx, "PQRS-TVWX")
	if err != nil {
		t.Fatal(err)
	}

	hook := &hookClient{Client: c}
	hook.afterGet = func() {
		if err := s.DenyDevice(ctx, found.Name); err != nil {
			t.Fatalf("setup: the concurrent deny must succeed: %v", err)
		}
	}
	racing := newTestStore(hook, testNow, testSettings(), nil)

	if err := racing.ApproveDevice(ctx, found.Name, methods.Subject{User: *testUser("alice")}); !errors.Is(err, errDeviceDecided) {
		t.Fatalf("a stale-read approve racing a concurrent deny must be refused, got %v", err)
	}
	state, err := s.GetDeviceAuthorizatonState(ctx, "bedrock-cli", "device-code-race")
	if err != nil || !state.Denied || state.Done {
		t.Fatalf("the denied decision must win the race, got %+v, err %v", state, err)
	}
}

func TestExpiredDeviceRequest(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	expires := testNow.Add(10 * time.Minute)
	if err := s.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-1", "WXZB-CDFG", expires, []string{"openid"}); err != nil {
		t.Fatal(err)
	}
	found, err := s.DeviceRequestByUserCode(ctx, "WXZB-CDFG")
	if err != nil {
		t.Fatal(err)
	}
	late := newTestStore(c, expires, testSettings(), nil)
	state, err := late.GetDeviceAuthorizatonState(ctx, "bedrock-cli", "device-code-1")
	if err != nil || state.Done || state.Denied || state.Expires.After(expires) {
		t.Fatalf("expired state %+v, err %v", state, err)
	}
	if _, err := late.DeviceRequestByUserCode(ctx, "WXZB-CDFG"); !isNotFound(err) {
		t.Fatalf("an expired user code must be not found, got %v", err)
	}
	if err := late.ApproveDevice(ctx, found.Name, methods.Subject{User: *testUser("alice")}); !isNotFound(err) {
		t.Fatalf("an expired request must not be approved, got %v", err)
	}
	if err := late.StoreDeviceAuthorization(ctx, "bedrock-cli", "device-code-2", "WXZB-CDFG", expires.Add(10*time.Minute), []string{"openid"}); err != nil {
		t.Fatalf("the user code of an expired request is free again: %v", err)
	}
}
