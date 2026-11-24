package saxess

import (
	"context"
	"encoding/json"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const stateKey = "state.json"

var errInvalidState = errors.New("invalid sAxess state Secret")

// SecretStore uses a dedicated, pre-provisioned Secret. Pass an uncached reader
// and grant the writer get/update access only to that Secret.
type SecretStore struct {
	reader client.Reader
	writer client.Writer
	key    client.ObjectKey
}

func NewSecretStore(reader client.Reader, writer client.Writer, key client.ObjectKey) *SecretStore {
	return &SecretStore{reader: reader, writer: writer, key: key}
}

func (s *SecretStore) View(ctx context.Context, view func(*State) error) error {
	_, state, err := s.load(ctx)
	if err != nil {
		return err
	}
	return view(state)
}

// Update commits state using the Secret's resource version. Apply may run more
// than once and must not perform side effects outside state.
func (s *SecretStore) Update(ctx context.Context, apply func(*State) error) error {
	return retry.OnError(retry.DefaultBackoff, apierrors.IsConflict, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		secret, state, err := s.load(ctx)
		if err != nil {
			return err
		}
		if err := apply(state); err != nil {
			return err
		}
		next, err := json.Marshal(state)
		if err != nil {
			return err
		}
		// shortcut: one Secret fits a small operator roster, shard storage before
		// supporting large directories or long-lived approval history.
		if len(next) > 512<<10 {
			return errors.New("sAxess state exceeds storage limit")
		}
		secret.Data[stateKey] = next
		return s.writer.Update(ctx, secret)
	})
}

func (s *SecretStore) load(ctx context.Context) (*corev1.Secret, *State, error) {
	var secret corev1.Secret
	if err := s.reader.Get(ctx, s.key, &secret); err != nil {
		return nil, nil, err
	}
	var state *State
	if err := json.Unmarshal(secret.Data[stateKey], &state); err != nil || state == nil {
		return nil, nil, errInvalidState
	}
	return &secret, state, nil
}
