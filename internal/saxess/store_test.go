package saxess

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSecretStoreRetriesAgainstNewResourceVersion(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: "saxess-state"},
		Data:       map[string][]byte{stateKey: []byte("{}\n")},
	}
	c := fake.NewClientBuilder().WithObjects(secret).Build()
	key := client.ObjectKeyFromObject(secret)
	w := &conflictingWriter{Client: c}
	s := NewSecretStore(c, w, key)
	calls := 0
	err := s.Update(ctx, func(*State) error {
		calls++
		return nil
	})
	if !errors.Is(err, errInvalidState) || calls != 1 || w.writes != 1 {
		t.Fatalf("result = %v, callbacks = %d, writes = %d", err, calls, w.writes)
	}
	if err := c.Get(ctx, key, secret); err != nil || string(secret.Data[stateKey]) != "null" {
		t.Fatalf("competing update lost or rejected update committed: %+v, %v", secret, err)
	}
}

type conflictingWriter struct {
	client.Client
	writes int
}

func (w *conflictingWriter) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	w.writes++
	if w.writes == 1 {
		var competing corev1.Secret
		if err := w.Get(ctx, client.ObjectKeyFromObject(obj), &competing); err != nil {
			return err
		}
		competing.Data[stateKey] = []byte("null")
		if err := w.Client.Update(ctx, &competing); err != nil {
			return err
		}
	}
	return w.Client.Update(ctx, obj, opts...)
}
