package server

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
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
	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	testHost     = "example.test"
	testIssuer   = "https://sso.example.test"
	testCA       = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n"
	testBearer   = "test-bearer"
	testRedirect = "http://127.0.0.1:43123/callback"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

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
	return c, cfg
}

func seedCluster(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	objects := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}},
		&v1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName},
			Spec:       v1alpha1.ClusterSpec{DesiredVersion: "v0.2.0", API: v1alpha1.APISpec{VIP: "10.0.0.10"}},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: release.SystemNamespace},
			Data:       map[string]string{"ca.crt": testCA},
		},
	}
	for _, obj := range objects {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	if err := settings.Seed(ctx, c); err != nil {
		t.Fatal(err)
	}
	setSetting(t, c, "platform.host", testHost)
}

func setSetting(t *testing.T, c client.Client, key, value string) {
	t.Helper()
	var setting v1alpha1.Setting
	if err := c.Get(context.Background(), client.ObjectKey{Name: key}, &setting); err != nil {
		t.Fatal(err)
	}
	setting.Spec.Value = value
	if err := c.Update(context.Background(), &setting); err != nil {
		t.Fatal(err)
	}
}

func saveSigningKey(t *testing.T, c client.Client, notBefore time.Time) keys.Key {
	t.Helper()
	key, err := keys.Generate(rand.Reader, notBefore)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Save(context.Background(), c, key); err != nil {
		t.Fatal(err)
	}
	return key
}

func createCLIClient(t *testing.T, c client.Client) {
	t.Helper()
	oauth := &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Name: "bedrock-cli", Namespace: release.SystemNamespace},
		Spec: v1alpha1.OAuthClientSpec{
			ClientID:     "bedrock-cli",
			Public:       true,
			RedirectURIs: []string{"http://127.0.0.1/callback"},
			GrantTypes:   []string{v1alpha1.GrantAuthorizationCode, v1alpha1.GrantRefreshToken, v1alpha1.GrantDeviceCode},
		},
	}
	if err := c.Create(context.Background(), oauth); err != nil {
		t.Fatal(err)
	}
}

func newTestServer(t *testing.T, cfg *rest.Config, clock func() time.Time) *httptest.Server {
	t.Helper()
	handler, err := New(context.Background(), Config{
		RestConfig:    cfg,
		Random:        rand.Reader,
		Clock:         clock,
		UI:            fstest.MapFS{"index.html": {Data: []byte(indexPage)}},
		WebhookSecret: func(context.Context) (string, error) { return testBearer, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func noRedirects(server *httptest.Server, jar http.CookieJar) *http.Client {
	return &http.Client{
		Transport:     server.Client().Transport,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func request(t *testing.T, c *http.Client, server *httptest.Server, method, host, target string, body io.Reader, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+target, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	for name, values := range header {
		req.Header[name] = values
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}
