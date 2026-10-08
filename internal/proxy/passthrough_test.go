package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type seenRequest struct {
	Method string
	URI    string
	Header http.Header
	Body   string
	Certs  int
}

type fakeAPI struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []seenRequest
}

func newFakeAPI(t *testing.T, handler http.HandlerFunc) *fakeAPI {
	t.Helper()
	api := &fakeAPI{}
	api.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		api.mu.Lock()
		api.requests = append(api.requests, seenRequest{Method: r.Method, URI: r.RequestURI, Header: r.Header.Clone(), Body: string(body), Certs: len(r.TLS.PeerCertificates)})
		api.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		handler(w, r)
	}))
	api.server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	api.server.StartTLS()
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeAPI) seen() []seenRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]seenRequest{}, a.requests...)
}

func (a *fakeAPI) restConfig() *rest.Config {
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.server.Certificate().Raw})
	return &rest.Config{Host: a.server.URL, TLSClientConfig: rest.TLSClientConfig{CAData: cert}}
}

func (a *fakeAPI) upstream(t *testing.T) *Upstream {
	t.Helper()
	u, err := NewUpstream(a.restConfig())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func answerOK(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func asCaller(handler http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), Caller{Token: token})))
	})
}

func do(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func clientCertificatePEM(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "proxy-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestPassThroughNeverSendsProxyCredentials(t *testing.T) {
	api := newFakeAPI(t, answerOK)
	cfg := api.restConfig()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("proxy-sa-file-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.BearerToken = "proxy-sa-token"
	cfg.BearerTokenFile = tokenFile
	cfg.CertData, cfg.KeyData = clientCertificatePEM(t)
	u, err := NewUpstream(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, handler := range []http.Handler{asCaller(ConsolePassThrough(u), "caller-token"), asCaller(APIPassThrough(u), "caller-token")} {
		if rec := do(handler, httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil)); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	}
	for _, seen := range api.seen() {
		if seen.Header.Get("Authorization") != "Bearer caller-token" || seen.Certs != 0 {
			t.Fatalf("upstream saw %q and %d client certificates", seen.Header.Get("Authorization"), seen.Certs)
		}
	}
	if len(api.seen()) != 2 {
		t.Fatalf("upstream saw %d requests", len(api.seen()))
	}
}

func TestPassThroughRefusesRequestsWithoutACaller(t *testing.T) {
	api := newFakeAPI(t, answerOK)
	rec := do(ConsolePassThrough(api.upstream(t)), httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil))
	if rec.Code != http.StatusUnauthorized || len(api.seen()) != 0 {
		t.Fatalf("status %d, upstream saw %d requests", rec.Code, len(api.seen()))
	}
}

func TestConsolePassThroughStripsImpersonation(t *testing.T) {
	api := newFakeAPI(t, answerOK)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil)
	for name, value := range map[string]string{
		"Impersonate-User": "root", "Impersonate-Group": "system:masters", "Impersonate-Uid": "1", "Impersonate-Extra-Scopes": "x",
		"Cookie": "__Host-bedrock_console=secret", "X-Remote-User": "root", "X-Remote-Group": "system:masters",
		"Forwarded": "for=6.6.6.6", "Authorization": "Bearer browser-token",
	} {
		req.Header.Set(name, value)
	}
	do(asCaller(ConsolePassThrough(api.upstream(t)), "caller-token"), req)
	header := api.seen()[0].Header
	for _, name := range []string{"Impersonate-User", "Impersonate-Group", "Impersonate-Uid", "Impersonate-Extra-Scopes", "Cookie", "X-Remote-User", "X-Remote-Group", "Forwarded"} {
		if header.Get(name) != "" {
			t.Errorf("%s reached the API server: %q", name, header.Get(name))
		}
	}
	if header.Get("Authorization") != "Bearer caller-token" {
		t.Errorf("Authorization %q", header.Get("Authorization"))
	}
}

func TestAPIPassThroughKeepsImpersonation(t *testing.T) {
	api := newFakeAPI(t, answerOK)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil)
	req.Header.Set("Impersonate-User", "someone")
	req.Header.Set("Cookie", "x=y")
	do(asCaller(APIPassThrough(api.upstream(t)), "caller-token"), req)
	header := api.seen()[0].Header
	if header.Get("Impersonate-User") != "someone" || header.Get("Cookie") != "" {
		t.Fatalf("headers %v", header)
	}
}

func TestPassThroughRewritesForwardedFor(t *testing.T) {
	api := newFakeAPI(t, answerOK)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 10.0.0.9")
	do(asCaller(ConsolePassThrough(api.upstream(t)), "caller-token"), req)
	if got := api.seen()[0].Header.Values("X-Forwarded-For"); len(got) != 1 || got[0] != "10.0.0.9" {
		t.Fatalf("X-Forwarded-For %v", got)
	}
}

func TestPassThroughOnlyForwardsKubernetesPaths(t *testing.T) {
	api := newFakeAPI(t, answerOK)
	handler := asCaller(ConsolePassThrough(api.upstream(t)), "caller-token")
	for _, target := range []string{"/api/v1/namespaces", "/apis/bedrock.cloudyfolks.io/v1alpha1/settings", "/openapi/v3", "/version", "/api", "/apis"} {
		if rec := do(handler, httptest.NewRequest(http.MethodGet, target, nil)); rec.Code != http.StatusOK {
			t.Errorf("%s: %d", target, rec.Code)
		}
	}
	forwarded := len(api.seen())
	for _, target := range []string{"/metrics", "/debug/pprof/", "/logs/", "/healthz", "/api/../metrics", "/apis/%2e%2e/metrics", "/apis/a%2fb", "/"} {
		if rec := do(handler, httptest.NewRequest(http.MethodGet, target, nil)); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", target, rec.Code)
		}
	}
	if len(api.seen()) != forwarded {
		t.Fatalf("upstream saw %d requests for refused paths", len(api.seen())-forwarded)
	}
}

func TestPassThroughKeepsMethodBodyAndQuery(t *testing.T) {
	api := newFakeAPI(t, answerOK)
	req := httptest.NewRequest(http.MethodPatch, "/apis/bedrock.cloudyfolks.io/v1alpha1/settings/x?fieldManager=bedrock-panel&dryRun=All", strings.NewReader(`{"spec":{"value":"1"}}`))
	req.Header.Set("Content-Type", "application/merge-patch+json")
	do(asCaller(ConsolePassThrough(api.upstream(t)), "caller-token"), req)
	seen := api.seen()[0]
	if seen.Method != http.MethodPatch || seen.URI != "/apis/bedrock.cloudyfolks.io/v1alpha1/settings/x?fieldManager=bedrock-panel&dryRun=All" ||
		seen.Header.Get("Content-Type") != "application/merge-patch+json" || seen.Body != `{"spec":{"value":"1"}}` {
		t.Fatalf("upstream saw %+v", seen)
	}
}

func TestPassThroughStreamsWatch(t *testing.T) {
	release := make(chan struct{})
	api := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("one\n"))
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte("two\n"))
	})
	front := httptest.NewServer(asCaller(ConsolePassThrough(api.upstream(t)), "caller-token"))
	t.Cleanup(front.Close)
	resp, err := http.Get(front.URL + "/api/v1/namespaces?watch=true")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 4)
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(resp.Body, first)
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil || string(first) != "one\n" {
			t.Fatalf("first chunk %q, %v", first, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first chunk did not arrive before the second was written")
	}
	close(release)
}

func TestPassThroughKeepsStatusCodes(t *testing.T) {
	for _, code := range []int{401, 403, 404, 409, 410, 422} {
		body := `{"kind":"Status","code":` + http.StatusText(code) + `}`
		api := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		})
		rec := do(asCaller(ConsolePassThrough(api.upstream(t)), "caller-token"), httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil))
		if rec.Code != code || rec.Body.String() != body {
			t.Errorf("%d: got %d %q", code, rec.Code, rec.Body.String())
		}
	}
}

func TestPassThroughUpgradesWebSocket(t *testing.T) {
	upgrader := websocket.Upgrader{}
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		kind, message, err := conn.ReadMessage()
		if err == nil {
			_ = conn.WriteMessage(kind, message)
		}
	})
	front := httptest.NewServer(asCaller(ConsolePassThrough(api.upstream(t)), "caller-token"))
	t.Cleanup(front.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(front.URL, "http")+"/api/v1/namespaces/x/pods/y/exec", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, echoed, err := conn.ReadMessage()
	if err != nil || string(echoed) != "ping" {
		t.Fatalf("echo %q, %v", echoed, err)
	}
}

func TestConsolePassThroughSetsNoStore(t *testing.T) {
	cached := false
	api := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		if cached {
			w.Header().Set("Cache-Control", "public, immutable")
		}
		_, _ = w.Write([]byte("{}"))
	})
	handler := asCaller(ConsolePassThrough(api.upstream(t)), "caller-token")
	if got := do(handler, httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil)).Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control %q", got)
	}
	cached = true
	if got := do(handler, httptest.NewRequest(http.MethodGet, "/openapi/v3?hash=1", nil)).Header().Get("Cache-Control"); got != "public, immutable" {
		t.Fatalf("Cache-Control %q", got)
	}
}

func TestPassThroughDropsSetCookie(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "upstream", Value: "x"})
		_, _ = w.Write([]byte("{}"))
	})
	rec := do(asCaller(ConsolePassThrough(api.upstream(t)), "caller-token"), httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil))
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie %q", rec.Header().Get("Set-Cookie"))
	}
}

func TestPassThroughUpstreamDownAnswers502(t *testing.T) {
	api := newFakeAPI(t, answerOK)
	u := api.upstream(t)
	api.server.Close()
	rec := do(asCaller(ConsolePassThrough(u), "caller-token"), httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"upstream_unavailable"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestRealAPIServerHonorsTheCallerIdentity(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-test-settings-reader"},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"settings"}, Verbs: []string{"list"}}},
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-test-settings-reader"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role.Name},
		Subjects:   []rbacv1.Subject{{APIGroup: "rbac.authorization.k8s.io", Kind: "User", Name: "bedrock:alice"}},
	}
	if err := c.Create(ctx, role); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	u, err := NewUpstream(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	target := "/apis/bedrock.cloudyfolks.io/v1alpha1/settings"
	alice := asCaller(ConsolePassThrough(u), "token-alice")
	var rec *httptest.ResponseRecorder
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		rec = do(alice, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusTooManyRequests {
			break
		}
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(asCaller(ConsolePassThrough(u), "token-bob"), httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"Status"`) {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
}
