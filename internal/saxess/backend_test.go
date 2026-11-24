package saxess

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"
)

type testStateStore struct {
	mu          sync.Mutex
	state       State
	beforeRetry func()
	afterCommit func()
}

func (s *testStateStore) View(_ context.Context, fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(&s.state)
}

func (s *testStateStore) Update(_ context.Context, fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		state := State{
			Operators: maps.Clone(s.state.Operators), Invites: maps.Clone(s.state.Invites),
			Devices: maps.Clone(s.state.Devices), Tokens: maps.Clone(s.state.Tokens),
			Challenges: maps.Clone(s.state.Challenges), Audit: maps.Clone(s.state.Audit),
		}
		if err := fn(&state); err != nil {
			return err
		}
		if s.beforeRetry != nil {
			s.beforeRetry()
			s.beforeRetry = nil
			continue
		}
		s.state = state
		if s.afterCommit != nil {
			s.afterCommit()
			s.afterCommit = nil
		}
		return nil
	}
}

func newTestBackend() (*Backend, *testStateStore, *time.Time) {
	store := &testStateStore{}
	now := time.Unix(42, 0)
	b := NewBackend(store, func(key, payload, signature string) error {
		if signature != testSignature(key, payload) {
			return errors.New("bad proof")
		}
		return nil
	})
	b.now = func() time.Time { return now }
	return b, store, &now
}

func testSignature(key, payload string) string {
	return key + ":" + payload
}

func testInvite(t *testing.T, b *Backend, operator string) EnrollmentInvite {
	t.Helper()
	invite, err := b.CreateInvite(context.Background(), operator, operator+"@example.com", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return invite
}

func testRegister(t *testing.T, b *Backend, operator, deviceID string) string {
	t.Helper()
	invite := testInvite(t, b, operator)
	key := "key-" + deviceID
	registered, err := b.RegisterDevice(context.Background(), DeviceRegistration{
		InviteToken: invite.Token, InvitePayload: invite.Payload, DeviceID: deviceID, Fingerprint: deviceID,
		DeviceType: "ios", PublicKey: key, Signature: testSignature(key, invite.Payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return registered.Token
}

func testApprove(t *testing.T, b *Backend, operator, deviceID string) MobilePrincipal {
	t.Helper()
	ctx := context.Background()
	token := testRegister(t, b, operator, deviceID)
	if err := b.ApproveDevice(ctx, operator, deviceID, "key-"+deviceID); err != nil {
		t.Fatal(err)
	}
	p, err := b.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBackendRecoversLostRegistrationResponse(t *testing.T) {
	ctx := context.Background()
	b, _, _ := newTestBackend()
	token := testRegister(t, b, "operator", "phone")
	if err := b.RevokeDevice(ctx, "operator", "phone"); err != nil {
		t.Fatal(err)
	}
	invite := testInvite(t, b, "operator")
	newDevice, err := b.RegisterDevice(ctx, DeviceRegistration{
		InviteToken: invite.Token, InvitePayload: invite.Payload, DeviceID: "replacement", Fingerprint: "phone",
		DeviceType: "ios", PublicKey: "key-phone", Signature: testSignature("key-phone", invite.Payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ApproveDevice(ctx, "operator", "replacement", "key-phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Authenticate(ctx, token); !errors.Is(err, ErrMobileUnauthorized) {
		t.Fatalf("revoked device authenticated: %v", err)
	}
	if p, err := b.Authenticate(ctx, newDevice.Token); err != nil || p.DeviceID != "replacement" {
		t.Fatalf("replacement failed: %+v, %v", p, err)
	}
}

func TestBackendEnrollment(t *testing.T) {
	ctx := context.Background()
	b, store, now := newTestBackend()
	invite := testInvite(t, b, "operator")
	reg := DeviceRegistration{
		InviteToken: invite.Token, InvitePayload: invite.Payload, DeviceID: "phone", DeviceType: "android",
		Fingerprint: "fingerprint", PublicKey: "key", Signature: "bad-proof",
	}
	if _, ok := store.state.Invites[invite.Token]; ok || invite.Token == invite.Payload {
		t.Fatal("invite token exposed in state or signing payload")
	}
	if _, err := b.RegisterDevice(ctx, reg); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("invalid proof: %v", err)
	}
	reg.Signature = testSignature(reg.PublicKey, invite.Payload)
	reg.InvitePayload = strings.Repeat("a", 64)
	if _, err := b.RegisterDevice(ctx, reg); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("mismatched invite payload: %v", err)
	}
	reg.InvitePayload = invite.Payload
	registered, err := b.RegisterDevice(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	token := registered.Token
	if d := store.state.Devices[reg.DeviceID]; d.DeviceType != reg.DeviceType ||
		d.Fingerprint != reg.Fingerprint || d.RegisteredAt.IsZero() || !d.RegisteredAt.Equal(registered.RegisteredAt) {
		t.Fatalf("metadata not persisted: %+v", d)
	}
	if _, ok := store.state.Tokens[token]; ok || token == invite.Token || token == invite.Payload {
		t.Fatal("bearer token exposed in state or reused enrollment data")
	}
	if _, err := b.RegisterDevice(ctx, reg); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("replayed invite: %v", err)
	}
	if _, err := b.Authenticate(ctx, token); !errors.Is(err, ErrMobileUnauthorized) {
		t.Fatalf("pending device authenticated: %v", err)
	}
	if err := b.ApproveDevice(ctx, "operator", "phone", "another-key"); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("changed key approved: %v", err)
	}
	if err := b.ApproveDevice(ctx, "operator", "phone", "key"); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Authenticate(ctx, token); err != nil || got != (MobilePrincipal{"operator", "operator@example.com", "phone"}) {
		t.Fatalf("approved principal: %+v, %v", got, err)
	}
	if _, err := b.CreateInvite(ctx, "other", "OPERATOR@example.com", time.Minute); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("email reassigned: %v", err)
	}
	other := testInvite(t, b, "other")
	reg.InviteToken, reg.InvitePayload, reg.Signature = other.Token, other.Payload, testSignature(reg.PublicKey, other.Payload)
	if _, err := b.RegisterDevice(ctx, reg); !errors.Is(err, ErrRegistrationConflict) {
		t.Fatalf("device reassigned: %v", err)
	}
	reg.DeviceID = "other-phone"
	if _, err := b.RegisterDevice(ctx, reg); !errors.Is(err, ErrRegistrationConflict) {
		t.Fatalf("fingerprint reassigned: %v", err)
	}

	*now = other.ExpiresAt
	reg.Fingerprint = "other-fingerprint"
	if _, err := b.RegisterDevice(ctx, reg); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("expired invite registered: %v", err)
	}
	testInvite(t, b, "other")
	if _, ok := store.state.Invites[credentialDigest(other.Token)]; ok {
		t.Fatal("expired invite retained after write")
	}
	if err := b.DisableOperator(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Authenticate(ctx, token); !errors.Is(err, ErrMobileUnauthorized) {
		t.Fatalf("disabled operator authenticated: %v", err)
	}
	if _, err := b.CreateInvite(ctx, "operator", "operator@example.com", time.Minute); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("invite reactivated disabled operator: %v", err)
	}
}

func TestBackendConsentAndConsume(t *testing.T) {
	ctx := context.Background()
	b, store, _ := newTestBackend()
	owner := testApprove(t, b, "owner", "owner-phone")
	other := testApprove(t, b, "other", "other-phone")
	secondPhone := testApprove(t, b, "owner", "second-phone")
	challenge, err := b.CreateChallenge(ctx, "owner", "private-browser-transaction", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := b.GetRequests(ctx, owner)
	if err != nil || len(requests) != 1 {
		t.Fatalf("owner requests: %+v, %v", requests, err)
	}
	r := requests[0]
	if r.AuthReqID != challenge.Nonce || r.Notify.Data.AuthReqID != r.AuthReqID ||
		r.Notify.Data.SignPayload != r.AuthReqID || r.Status != "pending" || strings.Contains(r.AuthReqID, "private-browser") {
		t.Fatalf("mobile request: %+v", r)
	}
	if requests, err := b.GetRequests(ctx, other); err != nil || len(requests) != 0 {
		t.Fatalf("another operator saw request: %+v, %v", requests, err)
	}
	msg := MobileConsent{AuthReqID: r.AuthReqID, DeviceID: owner.DeviceID, Email: owner.Email, SignedMsg: "wrong payload"}
	if err := b.SendConsent(ctx, owner, msg); !errors.Is(err, ErrMobileConsentRejected) {
		t.Fatalf("invalid proof accepted: %v", err)
	}
	msg.SignedMsg = testSignature("key-"+owner.DeviceID, r.Notify.Data.SignPayload)
	if err := b.SendConsent(ctx, secondPhone, msg); !errors.Is(err, ErrMobileConsentRejected) {
		t.Fatalf("another device's token accepted: %v", err)
	}
	if err := b.SendConsent(ctx, owner, msg); err != nil {
		t.Fatal(err)
	}
	if requests, err := b.GetRequests(ctx, owner); err != nil || len(requests) != 1 || requests[0].Status != "verified" {
		t.Fatalf("approved wire status: %+v, %v", requests, err)
	}
	if audit := store.state.Audit[r.AuthReqID]; audit.OperatorID != owner.EmployeeID ||
		audit.DeviceID != owner.DeviceID || audit.Proof != msg.SignedMsg {
		t.Fatalf("proof and audit missing: %+v", audit)
	}
	if err := b.SendConsent(ctx, owner, msg); !errors.Is(err, ErrMobileConsentRejected) {
		t.Fatalf("proof replay accepted: %v", err)
	}
	if _, err := b.ConsumeChallenge(ctx, "owner", "other-browser", r.AuthReqID); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("wrong browser consumed: %v", err)
	}
	if operator, err := b.ConsumeChallenge(ctx, "owner", challenge.BrowserTransactionID, r.AuthReqID); err != nil || operator != "owner" {
		t.Fatalf("consume: %q, %v", operator, err)
	}
	if requests, err := b.GetRequests(ctx, owner); err != nil || len(requests) != 1 || requests[0].Status != "used" {
		t.Fatalf("consumed wire status: %+v, %v", requests, err)
	}
	if _, err := b.ConsumeChallenge(ctx, "owner", challenge.BrowserTransactionID, r.AuthReqID); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("double consume: %v", err)
	}
}

func TestBackendConsumeFailsClosedOnRetryLatencyOrRevocation(t *testing.T) {
	for _, mode := range []string{"retry", "commit latency", "revocation"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			b, store, now := newTestBackend()
			p := testApprove(t, b, "owner", "phone")
			c, err := b.CreateChallenge(ctx, "owner", "browser", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			consent := MobileConsent{AuthReqID: c.Nonce, DeviceID: p.DeviceID, Email: p.Email,
				SignedMsg: testSignature("key-phone", c.Nonce)}
			if err := b.SendConsent(ctx, p, consent); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "retry":
				store.beforeRetry = func() { *now = c.ExpiresAt }
			case "commit latency":
				store.afterCommit = func() { *now = c.ExpiresAt }
			case "revocation":
				if err := b.RevokeDevice(ctx, p.EmployeeID, p.DeviceID); err != nil {
					t.Fatal(err)
				}
				if _, err := b.GetRequests(ctx, p); !errors.Is(err, ErrMobileUnauthorized) {
					t.Fatalf("stale principal could read: %v", err)
				}
			}
			if operator, err := b.ConsumeChallenge(ctx, "owner", "browser", c.Nonce); !errors.Is(err, ErrInvalidChallenge) || operator != "" {
				t.Fatalf("expired or revoked approval consumed: %q, %v", operator, err)
			}
		})
	}
}
