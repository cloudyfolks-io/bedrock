package operator

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func createValid(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	if err := c.Create(context.Background(), obj); err != nil {
		t.Fatalf("create %T %q: %v", obj, obj.GetName(), err)
	}
}

func createInvalid(t *testing.T, c client.Client, obj client.Object, want string) {
	t.Helper()
	requireInvalid(t, c.Create(context.Background(), obj), want)
}

func updateInvalid(t *testing.T, c client.Client, obj client.Object, want string) {
	t.Helper()
	requireInvalid(t, c.Update(context.Background(), obj), want)
}

func requireInvalid(t *testing.T, err error, want string) {
	t.Helper()
	if !apierrors.IsInvalid(err) {
		t.Fatalf("want an invalid error containing %q, got %v", want, err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("want error containing %q, got %v", want, err)
	}
}
