package store

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
)

func TestStoreKeySetMatchesPublished(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	day := 24 * time.Hour
	saved := make([]keys.Key, 0, 4)
	for _, start := range []time.Time{testNow.Add(-70 * day), testNow.Add(-40 * day), testNow.Add(-10 * day), testNow.Add(12 * time.Hour)} {
		key, err := keys.Generate(rand.Reader, start)
		if err != nil {
			t.Fatal(err)
		}
		if err := keys.Save(ctx, c, key); err != nil {
			t.Fatal(err)
		}
		saved = append(saved, key)
	}
	s := New(Config{
		Client:   c,
		Reader:   c,
		Random:   rand.Reader,
		Clock:    func() time.Time { return testNow },
		Settings: func(context.Context) (policy.Settings, error) { return testSettings(), nil },
		Keys:     func(ctx context.Context) ([]keys.Key, error) { return keys.Load(ctx, c) },
	})

	set, err := s.KeySet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	published := make([]string, 0, len(set))
	for _, key := range set {
		if key.Algorithm() != jose.ES256 || key.Use() != "sig" {
			t.Fatalf("key %s: algorithm %s use %s", key.ID(), key.Algorithm(), key.Use())
		}
		published = append(published, key.ID())
	}
	if want := []string{saved[1].ID, saved[2].ID, saved[3].ID}; !reflect.DeepEqual(published, want) {
		t.Fatalf("JWKS %v, want %v (retired but kept, active, future; not the expired one)", published, want)
	}

	signing, err := s.SigningKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if signing.ID() != saved[2].ID || signing.SignatureAlgorithm() != jose.ES256 {
		t.Fatalf("signing key %s %s, want %s", signing.ID(), signing.SignatureAlgorithm(), saved[2].ID)
	}
	signer, err := op.SignerFromKey(signing)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign([]byte(`{"sub":"alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	compact, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Signatures[0].Header.KeyID != saved[2].ID {
		t.Fatalf("kid %q, want %q", parsed.Signatures[0].Header.KeyID, saved[2].ID)
	}
	if payload, err := parsed.Verify(set[1].Key()); err != nil || string(payload) != `{"sub":"alice"}` {
		t.Fatalf("the published key must verify the signature: payload %s err %v", payload, err)
	}
	algorithms, err := s.SignatureAlgorithms(ctx)
	if err != nil || !reflect.DeepEqual(algorithms, []jose.SignatureAlgorithm{jose.ES256}) {
		t.Fatalf("algorithms %v err %v", algorithms, err)
	}
	if err := s.Health(ctx); err != nil {
		t.Fatalf("health with an active key: %v", err)
	}

	futureOnly := newTestStore(c, testNow, testSettings(), []keys.Key{saved[3]})
	if _, err := futureOnly.SigningKey(ctx); !errors.Is(err, errNoSigningKey) {
		t.Fatalf("a key that is only published cannot sign, got %v", err)
	}
	if err := futureOnly.Health(ctx); err == nil || err.Error() != "no active signing key" {
		t.Fatalf("health without an active key: %v", err)
	}
}
