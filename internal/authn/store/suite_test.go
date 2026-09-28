package store

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

var testNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func startTestEnv(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "manifests", "00-crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	return c
}

func testSettings() policy.Settings {
	return policy.Settings{SessionTTL: 12 * time.Hour, RefreshTTL: 720 * time.Hour, LockoutThreshold: 5, Host: "example.test", TLSMode: "SelfSigned"}
}

func newTestStore(c client.Client, now time.Time, settings policy.Settings, signing []keys.Key) *Store {
	return New(Config{
		Client:   c,
		Reader:   c,
		Random:   rand.Reader,
		Clock:    func() time.Time { return now },
		Settings: func(context.Context) (policy.Settings, error) { return settings, nil },
		Keys:     func(context.Context) ([]keys.Key, error) { return signing, nil },
	})
}

func create(t *testing.T, c client.Client, objects ...client.Object) {
	t.Helper()
	for _, obj := range objects {
		if err := c.Create(context.Background(), obj); err != nil {
			t.Fatalf("create %s: %v", obj.GetName(), err)
		}
	}
}

func testUser(name string) *v1alpha1.User {
	return &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: release.SystemNamespace},
		Spec: v1alpha1.UserSpec{
			Username:    name,
			DisplayName: "Test " + name,
			Email:       name + "@example.test",
			Methods:     []string{v1alpha1.MethodPassword},
		},
	}
}

func testGroup(name string, members ...string) *v1alpha1.Group {
	return &v1alpha1.Group{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: release.SystemNamespace},
		Spec:       v1alpha1.GroupSpec{Members: members},
	}
}
