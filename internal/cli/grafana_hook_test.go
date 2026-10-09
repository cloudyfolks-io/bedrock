package cli

import (
	"context"
	"crypto/rand"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestGrafanaAdminHookCreatesTheSecretOnlyForTheStackGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	hook := grafanaAdminHook(c, rand.Reader)
	key := client.ObjectKey{Namespace: "monitoring", Name: "bedrock-grafana-admin"}
	if err := hook(context.Background(), release.Group{Name: "traefik"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), key, &corev1.Secret{}); err == nil {
		t.Fatal("secret created for another group")
	}
	for range 2 {
		if err := hook(context.Background(), release.Group{Name: "kube-prometheus-stack"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Get(context.Background(), key, &corev1.Secret{}); err != nil {
		t.Fatal(err)
	}
}
