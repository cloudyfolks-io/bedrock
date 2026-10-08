package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

var userCounter atomic.Int32

type secretReads struct {
	client.Client
	reads atomic.Int32
}

func (s *secretReads) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, isSecret := obj.(*corev1.Secret); isSecret {
		s.reads.Add(1)
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

type resolverFixture struct {
	clock   *fakeClock
	fake    *fakeAuthn
	oidc    *OIDC
	store   *Store
	reads   *secretReads
	subject string
	sid     string
	seeded  atomic.Int32
	signer  *CookieSigner
}

func newResolverFixture(t *testing.T) *resolverFixture {
	t.Helper()
	c := startEnv(t)
	clock := newFakeClock()
	clock.now = time.Now().UTC().Truncate(time.Second)
	fake := newFakeAuthn(t)
	subject := "user-" + strconv.Itoa(int(userCounter.Add(1)))
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: subject, Namespace: "bedrock-system"},
		Spec:       v1alpha1.UserSpec{Username: subject},
	}
	if err := c.Create(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	sid := newAuthnSession(t, fake, 12*time.Hour)
	fake.mutate(func(f *fakeAuthn) { f.identity.Subject = subject })
	reads := &secretReads{Client: c}
	return &resolverFixture{
		clock: clock, fake: fake, store: NewStore(reads, clock.Now), reads: reads, subject: subject, sid: sid, signer: NewCookieSigner([]byte("0123456789abcdef0123456789abcdef")),
		oidc: newOIDCFor(fake, &mutableHost{value: "lab.example"}, fakeClientSecret),
	}
}

func (fx *resolverFixture) resolver(holder string) *Resolver {
	return NewResolver(fx.store, fx.oidc, fx.reads.Client, fx.clock.Now, holder, fx.signer)
}

func (fx *resolverFixture) seed(t *testing.T, accessLife time.Duration) (string, Record) {
	t.Helper()
	tokens, _ := signIn(t, fx.oidc, fx.fake)
	rec := Record{
		Subject: fx.subject, DisplayName: "Alice", Groups: []string{"bedrock-admins"}, SessionName: fx.sid,
		Access: tokens.Access, AccessExpiry: fx.clock.Now().Add(accessLife), Refresh: tokens.Refresh,
		CSRF: strings.Repeat("c", 43), CreatedAt: fx.clock.Now(), ExpiresAt: fx.clock.Now().Add(12 * time.Hour),
	}
	return fx.seedRecord(t, rec), rec
}

func (fx *resolverFixture) seedRecord(t *testing.T, rec Record) string {
	t.Helper()
	cookie := fx.signer.Sign("cookie-" + fx.subject + "-" + strconv.Itoa(int(fx.seeded.Add(1))))
	if err := fx.store.CreateSession(context.Background(), secret.SHA256Hex(cookie), rec); err != nil {
		t.Fatal(err)
	}
	return cookie
}

func TestResolveUsesTheCachedAccessToken(t *testing.T) {
	fx := newResolverFixture(t)
	cookie, rec := fx.seed(t, time.Hour)
	resolver := fx.resolver("proxy-a")
	before := fx.fake.Calls("token:refresh_token")
	first, err := resolver.Resolve(context.Background(), cookie)
	if err != nil || first.Access != rec.Access || first.Subject != fx.subject || !first.Admin || first.CSRF != rec.CSRF {
		t.Fatalf("first %+v, %v", first, err)
	}
	reads := fx.reads.reads.Load()
	fx.clock.Advance(4 * time.Second)
	if _, err := resolver.Resolve(context.Background(), cookie); err != nil {
		t.Fatal(err)
	}
	if fx.reads.reads.Load() != reads {
		t.Fatalf("Secret reads %d -> %d within the cache time", reads, fx.reads.reads.Load())
	}
	if fx.fake.Calls("token:refresh_token") != before {
		t.Fatal("cached token triggered a refresh")
	}
}

func TestResolveRefreshesWithinSixtySecondsOfExpiry(t *testing.T) {
	fx := newResolverFixture(t)
	resolver := fx.resolver("proxy-a")
	stale, old := fx.seed(t, 59*time.Second)
	got, err := resolver.Resolve(context.Background(), stale)
	if err != nil || got.Access == old.Access {
		t.Fatalf("resolve %+v, %v", got, err)
	}
	if fx.fake.Calls("token:refresh_token") != 1 {
		t.Fatalf("refresh calls %d", fx.fake.Calls("token:refresh_token"))
	}
	stored, err := fx.store.Session(context.Background(), secret.SHA256Hex(stale))
	if err != nil || stored.Access != got.Access || stored.Refresh == old.Refresh || stored.Claim != nil {
		t.Fatalf("stored %+v, %v", stored, err)
	}
	fresh, _ := fx.seed(t, 61*time.Second)
	if _, err := resolver.Resolve(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	if fx.fake.Calls("token:refresh_token") != 1 {
		t.Fatalf("refresh calls %d after a token with 61 seconds left", fx.fake.Calls("token:refresh_token"))
	}
}

func TestResolversRefreshOnceAcrossReplicas(t *testing.T) {
	fx := newResolverFixture(t)
	cookie, _ := fx.seed(t, 10*time.Second)
	replicas := []*Resolver{fx.resolver("proxy-a"), fx.resolver("proxy-b")}
	results := make([]string, 20)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := replicas[i%2].Resolve(context.Background(), cookie)
			if err != nil {
				t.Errorf("resolve %d: %v", i, err)
				return
			}
			results[i] = got.Access
		}(i)
	}
	wg.Wait()
	if fx.fake.Calls("token:refresh_token") != 1 || fx.fake.ReuseErrors() != 0 {
		t.Fatalf("refresh calls %d, reuse errors %d", fx.fake.Calls("token:refresh_token"), fx.fake.ReuseErrors())
	}
	for i, access := range results {
		if access == "" || access != results[0] {
			t.Fatalf("result %d = %q, want %q", i, access, results[0])
		}
	}
}

func TestStaleClaimIsTakenOver(t *testing.T) {
	fx := newResolverFixture(t)
	_, rec := fx.seed(t, 10*time.Second)
	rec.Claim = &Claim{Holder: "dead-pod", Until: fx.clock.Now().Add(-time.Second)}
	cookie := fx.seedRecord(t, rec)
	got, err := fx.resolver("proxy-a").Resolve(context.Background(), cookie)
	if err != nil || got.Access == rec.Access {
		t.Fatalf("resolve %+v, %v", got, err)
	}
}

func TestLiveClaimOfAnotherHolderMakesTheResolverWait(t *testing.T) {
	fx := newResolverFixture(t)
	_, rec := fx.seed(t, 10*time.Second)
	rec.Claim = &Claim{Holder: "other-pod", Until: fx.clock.Now().Add(claimLife)}
	cookie := fx.seedRecord(t, rec)
	hash := secret.SHA256Hex(cookie)
	before := fx.fake.Calls("token:refresh_token")
	baseline := fx.reads.reads.Load()
	go func() {
		waitUntilTrue(func() bool { return fx.reads.reads.Load() >= baseline+3 })
		done := rec
		done.Access, done.AccessExpiry, done.Refresh = "from-other-pod", fx.clock.Now().Add(time.Hour), "refresh-from-other-pod"
		_ = fx.store.CompleteRefresh(context.Background(), hash, "other-pod", done)
	}()
	got, err := fx.resolver("proxy-a").Resolve(context.Background(), cookie)
	if err != nil || got.Access != "from-other-pod" {
		t.Fatalf("resolve %+v, %v", got, err)
	}
	if fx.fake.Calls("token:refresh_token") != before {
		t.Fatal("resolver called authn while another holder worked")
	}
}

func TestResolveEndsWhenTheAuthnSessionIsDeleted(t *testing.T) {
	fx := newResolverFixture(t)
	cookie, _ := fx.seed(t, time.Hour)
	resolver := fx.resolver("proxy-a")
	if _, err := resolver.Resolve(context.Background(), cookie); err != nil {
		t.Fatal(err)
	}
	gone := &v1alpha1.Session{ObjectMeta: metav1.ObjectMeta{Name: fx.sid, Namespace: "bedrock-system"}}
	if err := testClient.Delete(context.Background(), gone); err != nil {
		t.Fatal(err)
	}
	fx.clock.Advance(6 * time.Second)
	if _, err := resolver.Resolve(context.Background(), cookie); err != nil {
		t.Fatalf("resolve inside the check time: %v", err)
	}
	fx.clock.Advance(10 * time.Second)
	if _, err := resolver.Resolve(context.Background(), cookie); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("resolve after the check time: %v", err)
	}
	if _, err := fx.store.Session(context.Background(), secret.SHA256Hex(cookie)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("record survived: %v", err)
	}
}

func TestResolveEndsForDisabledUser(t *testing.T) {
	fx := newResolverFixture(t)
	cookie, _ := fx.seed(t, time.Hour)
	var user v1alpha1.User
	key := client.ObjectKey{Namespace: "bedrock-system", Name: fx.subject}
	if err := testClient.Get(context.Background(), key, &user); err != nil {
		t.Fatal(err)
	}
	user.Spec.Disabled = true
	if err := testClient.Update(context.Background(), &user); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.resolver("proxy-a").Resolve(context.Background(), cookie); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("resolve: %v", err)
	}
}

func TestResolveEndsAfterSessionExpiry(t *testing.T) {
	fx := newResolverFixture(t)
	_, rec := fx.seed(t, time.Hour)
	rec.ExpiresAt = fx.clock.Now().Add(-time.Second)
	cookie := fx.seedRecord(t, rec)
	if _, err := fx.resolver("proxy-a").Resolve(context.Background(), cookie); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("resolve: %v", err)
	}
}

func TestResolveEndsOnInvalidGrant(t *testing.T) {
	fx := newResolverFixture(t)
	_, rec := fx.seed(t, 10*time.Second)
	rec.Refresh = "unknown-refresh-token"
	cookie := fx.seedRecord(t, rec)
	if _, err := fx.resolver("proxy-a").Resolve(context.Background(), cookie); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := fx.store.Session(context.Background(), secret.SHA256Hex(cookie)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("record survived: %v", err)
	}
}

func TestResolveKeepsTheRecordOnTransientAuthnFailure(t *testing.T) {
	fx := newResolverFixture(t)
	cookie, rec := fx.seed(t, 10*time.Second)
	fx.fake.mutate(func(f *fakeAuthn) { f.failStatus = 503 })
	if _, err := fx.resolver("proxy-a").Resolve(context.Background(), cookie); !errors.Is(err, ErrUpstream) {
		t.Fatalf("resolve: %v", err)
	}
	stored, err := fx.store.Session(context.Background(), secret.SHA256Hex(cookie))
	if err != nil || stored.Claim != nil || stored.Access != rec.Access || stored.Refresh != rec.Refresh {
		t.Fatalf("stored %+v, %v", stored, err)
	}
}

func TestResolveUnknownCookie(t *testing.T) {
	fx := newResolverFixture(t)
	resolver := fx.resolver("proxy-a")
	for _, cookie := range []string{"", "not-a-cookie"} {
		if _, err := resolver.Resolve(context.Background(), cookie); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("Resolve(%q) = %v", cookie, err)
		}
	}
}

func TestResolveNeverLogsTokens(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	fx := newResolverFixture(t)
	cookie, rec := fx.seed(t, 10*time.Second)
	fx.fake.mutate(func(f *fakeAuthn) { f.failStatus = 503 })
	_, _ = fx.resolver("proxy-a").Resolve(context.Background(), cookie)
	fx.fake.mutate(func(f *fakeAuthn) { f.failStatus = 0 })
	got, err := fx.resolver("proxy-a").Resolve(context.Background(), cookie)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{cookie, rec.Access, rec.Refresh, got.Access} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("log holds %q: %s", sensitive, logs.String())
		}
	}
}

func waitUntilTrue(condition func() bool) {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !condition(); time.Sleep(10 * time.Millisecond) {
	}
}

func TestResolveCachesUnknownCookiesBriefly(t *testing.T) {
	fx := newResolverFixture(t)
	resolver := fx.resolver("proxy-a")
	for i := 0; i < 2; i++ {
		if _, err := resolver.Resolve(context.Background(), fx.signer.Sign("unknown-cookie-value")); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal(err)
		}
	}
	reads := fx.reads.reads.Load()
	if _, err := resolver.Resolve(context.Background(), fx.signer.Sign("unknown-cookie-value")); !errors.Is(err, ErrUnauthenticated) || fx.reads.reads.Load() != reads {
		t.Fatalf("the third lookup read the Secret again: %d -> %d", reads, fx.reads.reads.Load())
	}
	fx.clock.Advance(4 * time.Second)
	if _, err := resolver.Resolve(context.Background(), fx.signer.Sign("unknown-cookie-value")); !errors.Is(err, ErrUnauthenticated) || fx.reads.reads.Load() == reads {
		t.Fatal("the miss was not looked up again after the cache time")
	}
}

func TestForgetEvictsTheCachedRecord(t *testing.T) {
	fx := newResolverFixture(t)
	cookie, _ := fx.seed(t, time.Hour)
	resolver := fx.resolver("proxy-a")
	if _, err := resolver.Resolve(context.Background(), cookie); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.DeleteSession(context.Background(), secret.SHA256Hex(cookie)); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), cookie); err != nil {
		t.Fatalf("the cache must still answer: %v", err)
	}
	resolver.Forget(cookie)
	if _, err := resolver.Resolve(context.Background(), cookie); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("after Forget: %v", err)
	}
}

func TestResolveRefusesUnsignedCookiesWithoutAnyRead(t *testing.T) {
	fx := newResolverFixture(t)
	resolver := fx.resolver("proxy-a")
	cookie, _ := fx.seed(t, time.Hour)
	id, mac, _ := strings.Cut(cookie, ".")
	otherKey := NewCookieSigner([]byte("ffffffffffffffffffffffffffffffff"))
	cases := map[string]string{
		"random value":   "random-cookie-value-without-a-mac",
		"bad mac":        id + "." + strings.Repeat("0", len(mac)),
		"id only":        id,
		"empty mac":      id + ".",
		"not hex":        id + ".zz",
		"before the key": otherKey.Sign(id),
	}
	reads := fx.reads.reads.Load()
	for name, value := range cases {
		if _, err := resolver.Resolve(context.Background(), value); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if fx.reads.reads.Load() != reads {
		t.Fatalf("rejected cookies caused %d Secret reads", fx.reads.reads.Load()-reads)
	}
	if _, err := resolver.Resolve(context.Background(), cookie); err != nil {
		t.Fatalf("the signed cookie must resolve: %v", err)
	}
}

func TestCookieKeyIsCreatedOnceAndReused(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	first, err := LoadCookieSigner(ctx, c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadCookieSigner(ctx, c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Verify(first.Sign("id-1")) {
		t.Fatal("the second load did not read the stored key")
	}
	if NewCookieSigner([]byte("another-key-another-key-another!")).Verify(first.Sign("id-1")) {
		t.Fatal("a signature verified under another key")
	}
}

type blindFirstGet struct {
	client.Client
	blinded atomic.Bool
}

func (b *blindFirstGet) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Name == CookieKeySecret && b.blinded.CompareAndSwap(false, true) {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
	}
	return b.Client.Get(ctx, key, obj, opts...)
}

func replaceCookieKey(t *testing.T, key []byte) {
	t.Helper()
	ctx := context.Background()
	stored := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: StoreNamespace, Name: CookieKeySecret}}
	if err := testClient.Delete(ctx, stored); err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	stored.Data = map[string][]byte{"key": key}
	if err := testClient.Create(ctx, stored); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testClient.Delete(ctx, &corev1.Secret{ObjectMeta: stored.ObjectMeta}) })
}

func TestCookieKeyCreateRaceReadsTheWinner(t *testing.T) {
	startEnv(t)
	winner := []byte("winner-key-winner-key-winner-key")
	replaceCookieKey(t, winner)
	loaded, err := LoadCookieSigner(context.Background(), &blindFirstGet{Client: testClient}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Verify(NewCookieSigner(winner).Sign("id-1")) {
		t.Fatal("the loser did not adopt the key of the winner")
	}
}

func TestCookieKeyRefusesAShortKey(t *testing.T) {
	startEnv(t)
	replaceCookieKey(t, []byte("short"))
	if _, err := LoadCookieSigner(context.Background(), testClient, rand.Reader); err == nil || !strings.Contains(err.Error(), "short key") {
		t.Fatalf("error = %v", err)
	}
}
