package store

import (
	"context"
	"errors"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
)

const keyUse = "sig"

var errNoSigningKey = errors.New("no active signing key")

type signingKey struct {
	key keys.Key
}

func (k signingKey) SignatureAlgorithm() jose.SignatureAlgorithm {
	return jose.ES256
}

func (k signingKey) Key() any {
	return k.key.Private
}

func (k signingKey) ID() string {
	return k.key.ID
}

type publicKey struct {
	key keys.Key
}

func (k publicKey) ID() string {
	return k.key.ID
}

func (k publicKey) Algorithm() jose.SignatureAlgorithm {
	return jose.ES256
}

func (k publicKey) Use() string {
	return keyUse
}

func (k publicKey) Key() any {
	return &k.key.Private.PublicKey
}

func (s *Store) SigningKey(ctx context.Context) (op.SigningKey, error) {
	all, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	key, ok := keys.Signing(all, s.clock())
	if !ok {
		return nil, errNoSigningKey
	}
	return signingKey{key: key}, nil
}

func (s *Store) SignatureAlgorithms(context.Context) ([]jose.SignatureAlgorithm, error) {
	return []jose.SignatureAlgorithm{jose.ES256}, nil
}

func (s *Store) KeySet(ctx context.Context) ([]op.Key, error) {
	all, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	published := keys.Published(all, s.clock())
	set := make([]op.Key, 0, len(published))
	for _, key := range published {
		set = append(set, publicKey{key: key})
	}
	return set, nil
}

func (s *Store) Health(ctx context.Context) error {
	_, err := s.SigningKey(ctx)
	return err
}
