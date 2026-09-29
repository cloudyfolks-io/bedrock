package account

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/store"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const testPassword = "correct horse battery staple"

type harness struct {
	client   client.Client
	store    *store.Store
	registry methods.Registry
	server   *httptest.Server
}

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

func newHarness(t *testing.T) harness {
	t.Helper()
	c := startTestEnv(t)
	settings := func(context.Context) (policy.Settings, error) {
		return policy.Settings{SessionTTL: 12 * time.Hour, RefreshTTL: 720 * time.Hour, LockoutThreshold: 5, Host: "example.test", TLSMode: "SelfSigned"}, nil
	}
	st := store.New(store.Config{
		Client:   c,
		Reader:   c,
		Random:   rand.Reader,
		Clock:    time.Now,
		Settings: settings,
		Keys:     func(context.Context) ([]keys.Key, error) { return nil, nil },
	})
	registry := methods.NewRegistry(
		methods.NewPassword(c, rand.Reader, settings),
		methods.NewTOTP(c, rand.Reader, func(context.Context) (string, error) { return "https://sso.example.test", nil }),
		methods.NewRecovery(c, rand.Reader),
	)
	server := httptest.NewTLSServer(Handler(Deps{Store: st, Client: c, Methods: registry, Settings: settings, Random: rand.Reader, Clock: time.Now, Limiter: methods.NewRateLimiter(100, time.Minute)}))
	t.Cleanup(server.Close)
	return harness{client: c, store: st, registry: registry, server: server}
}

func create(t *testing.T, c client.Client, objects ...client.Object) {
	t.Helper()
	for _, obj := range objects {
		if err := c.Create(context.Background(), obj); err != nil {
			t.Fatalf("create %s: %v", obj.GetName(), err)
		}
	}
}

func createUser(t *testing.T, h harness, username, source string) *v1alpha1.User {
	t.Helper()
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.UserObjectName(username), Namespace: release.SystemNamespace},
		Spec:       v1alpha1.UserSpec{Username: username, DisplayName: "User " + username, Email: username + "@example.test", Source: source, Methods: []string{v1alpha1.MethodPassword}},
	}
	create(t, h.client, user)
	if err := methods.SetPassword(context.Background(), h.client, rand.Reader, *user, testPassword); err != nil {
		t.Fatal(err)
	}
	return user
}

func loginAs(t *testing.T, h harness, user *v1alpha1.User) string {
	t.Helper()
	cookie, err := h.store.CreateSession(context.Background(), methods.Subject{User: *user, AMR: []string{"pwd"}}, "test-agent", "203.0.113.1")
	if err != nil {
		t.Fatal(err)
	}
	return cookie
}

func call(t *testing.T, h harness, method, path, cookie, csrf string, body any) *http.Response {
	t.Helper()
	var payload io.Reader = http.NoBody
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, h.server.URL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: login.CookieSession, Value: cookie})
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decode[T any](t *testing.T, resp *http.Response, status int) T {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("status %d, want %d: %s", resp.StatusCode, status, body)
	}
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return value
}

func errorOf(t *testing.T, resp *http.Response, status int) string {
	t.Helper()
	return decode[map[string]string](t, resp, status)["error"]
}

func noContent(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, want 204: %s", resp.StatusCode, body)
	}
}

func accountOf(t *testing.T, h harness, cookie string) accountView {
	t.Helper()
	return decode[accountView](t, call(t, h, http.MethodGet, "/api/v1/account", cookie, "", nil), http.StatusOK)
}

func exists(t *testing.T, c client.Client, obj client.Object, name string) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, obj)
	if client.IgnoreNotFound(err) != nil {
		t.Fatal(err)
	}
	return err == nil
}
