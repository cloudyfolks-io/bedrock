package proxy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	clock.now = time.Now().UTC().Truncate(time.Second)
	return NewStore(startEnv(t), clock.Now), clock
}

func sampleRecord(now time.Time) Record {
	return Record{
		Subject:      "alice",
		DisplayName:  "Alice",
		Email:        "alice@lab.example",
		Groups:       []string{"ops", "dev"},
		SessionName:  "sid-1",
		Access:       "access-1",
		AccessExpiry: now.Add(time.Hour),
		Refresh:      "refresh-1",
		CSRF:         strings.Repeat("c", 43),
		CreatedAt:    now,
		ExpiresAt:    now.Add(12 * time.Hour),
	}
}

func hashOf(name string) string { return secret.SHA256Hex(name) }

func TestStoreRoundTripsASession(t *testing.T) {
	store, clock := newTestStore(t)
	ctx := context.Background()
	want := sampleRecord(clock.Now())
	hash := hashOf("round-trip")
	if err := store.CreateSession(ctx, hash, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Session(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != want.Subject || got.DisplayName != want.DisplayName || got.Email != want.Email ||
		strings.Join(got.Groups, ",") != "ops,dev" || got.SessionName != want.SessionName || got.Access != want.Access ||
		!got.AccessExpiry.Equal(want.AccessExpiry) || got.Refresh != want.Refresh || got.CSRF != want.CSRF ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) || got.Claim != nil {
		t.Fatalf("round trip: %+v", got)
	}
	if _, err := store.Session(ctx, hashOf("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session error = %v", err)
	}
}

func TestSessionSecretNameAndLabels(t *testing.T) {
	store, clock := newTestStore(t)
	ctx := context.Background()
	cookie := "cookie-value-that-must-not-leak"
	hash := hashOf(cookie)
	if err := store.CreateSession(ctx, hash, sampleRecord(clock.Now())); err != nil {
		t.Fatal(err)
	}
	var stored corev1.Secret
	if err := testClient.Get(ctx, client.ObjectKey{Namespace: StoreNamespace, Name: "ps-" + hash}, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Name) != 3+64 || strings.Contains(stored.Name, cookie) {
		t.Fatalf("name %q", stored.Name)
	}
	if stored.Labels[v1alpha1.LabelKind] != "ProxySession" || stored.Labels[v1alpha1.LabelName] == "" {
		t.Fatalf("labels %v", stored.Labels)
	}
	managed := false
	for _, entry := range stored.ManagedFields {
		managed = managed || entry.Manager == v1alpha1.ProxyFieldManager
	}
	if !managed {
		t.Fatalf("managedFields %v", stored.ManagedFields)
	}
}

func TestClaimRefreshAllowsOneHolder(t *testing.T) {
	store, clock := newTestStore(t)
	ctx := context.Background()
	hash := hashOf("claim-race")
	if err := store.CreateSession(ctx, hash, sampleRecord(clock.Now())); err != nil {
		t.Fatal(err)
	}
	var won, lost atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.ClaimRefresh(ctx, hash, "pod-"+string(rune('a'+i)), clock.Now().Add(15*time.Second))
			switch {
			case err == nil:
				won.Add(1)
			case errors.Is(err, ErrClaimed):
				lost.Add(1)
			default:
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if won.Load() != 1 || lost.Load() != 19 {
		t.Fatalf("won %d lost %d", won.Load(), lost.Load())
	}
}

func TestExpiredClaimCanBeTaken(t *testing.T) {
	store, clock := newTestStore(t)
	ctx := context.Background()
	hash := hashOf("claim-expired")
	if err := store.CreateSession(ctx, hash, sampleRecord(clock.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRefresh(ctx, hash, "pod-a", clock.Now().Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRefresh(ctx, hash, "pod-b", clock.Now().Add(30*time.Second)); !errors.Is(err, ErrClaimed) {
		t.Fatalf("live claim error = %v", err)
	}
	clock.Advance(16 * time.Second)
	rec, err := store.ClaimRefresh(ctx, hash, "pod-b", clock.Now().Add(15*time.Second))
	if err != nil || rec.Claim == nil || rec.Claim.Holder != "pod-b" {
		t.Fatalf("take over: %+v, %v", rec, err)
	}
}

func TestCompleteRefreshClearsTheClaim(t *testing.T) {
	store, clock := newTestStore(t)
	ctx := context.Background()
	hash := hashOf("claim-complete")
	if err := store.CreateSession(ctx, hash, sampleRecord(clock.Now())); err != nil {
		t.Fatal(err)
	}
	rec, err := store.ClaimRefresh(ctx, hash, "pod-a", clock.Now().Add(15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	rec.Access, rec.Refresh = "access-2", "refresh-2"
	if err := store.CompleteRefresh(ctx, hash, rec); err != nil {
		t.Fatal(err)
	}
	got, err := store.Session(ctx, hash)
	if err != nil || got.Access != "access-2" || got.Refresh != "refresh-2" || got.Claim != nil {
		t.Fatalf("after complete: %+v, %v", got, err)
	}
}

func TestReleaseClaimKeepsTheTokens(t *testing.T) {
	store, clock := newTestStore(t)
	ctx := context.Background()
	hash := hashOf("claim-release")
	if err := store.CreateSession(ctx, hash, sampleRecord(clock.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRefresh(ctx, hash, "pod-a", clock.Now().Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseClaim(ctx, hash); err != nil {
		t.Fatal(err)
	}
	got, err := store.Session(ctx, hash)
	if err != nil || got.Claim != nil || got.Access != "access-1" || got.Refresh != "refresh-1" {
		t.Fatalf("after release: %+v, %v", got, err)
	}
}

func TestTakeLoginIsSingleUse(t *testing.T) {
	store, clock := newTestStore(t)
	ctx := context.Background()
	hash := hashOf("login-once")
	state := LoginState{Verifier: "v", Nonce: "n", ReturnTo: "/p", ExpiresAt: clock.Now().Add(10 * time.Minute)}
	if err := store.CreateLogin(ctx, hash, state); err != nil {
		t.Fatal(err)
	}
	got, err := store.TakeLogin(ctx, hash)
	if err != nil || got.Verifier != "v" || got.Nonce != "n" || got.ReturnTo != "/p" {
		t.Fatalf("first take: %+v, %v", got, err)
	}
	if _, err := store.TakeLogin(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second take error = %v", err)
	}
}

func TestTakeLoginRefusesExpiredState(t *testing.T) {
	store, clock := newTestStore(t)
	ctx := context.Background()
	hash := hashOf("login-expired")
	if err := store.CreateLogin(ctx, hash, LoginState{Verifier: "v", ExpiresAt: clock.Now().Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(11 * time.Minute)
	if _, err := store.TakeLogin(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired take error = %v", err)
	}
}

func TestSweepDeletesExpiredRecords(t *testing.T) {
	store, clock := newTestStore(t)
	ctx := context.Background()
	expiredLogin, liveLogin := hashOf("sweep-login-old"), hashOf("sweep-login-new")
	expiredSession, liveSession := hashOf("sweep-session-old"), hashOf("sweep-session-new")
	if err := store.CreateLogin(ctx, expiredLogin, LoginState{ExpiresAt: clock.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateLogin(ctx, liveLogin, LoginState{ExpiresAt: clock.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	old := sampleRecord(clock.Now())
	old.ExpiresAt = clock.Now().Add(-time.Minute)
	if err := store.CreateSession(ctx, expiredSession, old); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, liveSession, sampleRecord(clock.Now())); err != nil {
		t.Fatal(err)
	}
	other := &corev1.Secret{}
	other.Namespace, other.Name = StoreNamespace, "unrelated"
	if err := testClient.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Sweep(ctx)
	if err != nil || deleted < 2 {
		t.Fatalf("Sweep() = %d, %v", deleted, err)
	}
	if _, err := store.Session(ctx, expiredSession); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session survived: %v", err)
	}
	if _, err := store.Session(ctx, liveSession); err != nil {
		t.Fatalf("live session gone: %v", err)
	}
	if _, err := store.TakeLogin(ctx, expiredLogin); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired login survived: %v", err)
	}
	if _, err := store.TakeLogin(ctx, liveLogin); err != nil {
		t.Fatalf("live login gone: %v", err)
	}
	if err := testClient.Get(ctx, client.ObjectKeyFromObject(other), other); err != nil {
		t.Fatalf("unrelated secret gone: %v", err)
	}
}
