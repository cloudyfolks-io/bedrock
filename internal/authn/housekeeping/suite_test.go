package housekeeping

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

var t0 = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

func startTestEnv(t *testing.T) (client.Client, *rest.Config) {
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
	return c, cfg
}

func create(t *testing.T, c client.Client, objects ...client.Object) {
	t.Helper()
	for _, obj := range objects {
		if err := c.Create(context.Background(), obj); err != nil {
			t.Fatalf("create %s: %v", obj.GetName(), err)
		}
	}
}

func objectKey(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: release.SystemNamespace, Name: name}
}

func objectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: release.SystemNamespace}
}

func authRequest(name string, expiresAt metav1.Time) *v1alpha1.AuthRequest {
	return &v1alpha1.AuthRequest{
		ObjectMeta: objectMeta(name),
		Spec:       v1alpha1.AuthRequestSpec{ClientID: "bedrock-cli", RedirectURI: "http://127.0.0.1/callback", Scopes: []string{"openid"}, ResponseType: "code", ExpiresAt: expiresAt},
	}
}
