package login

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"sync"
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
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/authn/store"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const testPassword = "correct horse battery staple"

type harness struct {
	client   client.Client
	store    *store.Store
	server   *httptest.Server
	upstream *fakeUpstream
	reader   *hookClient
	handler  *hookClient
}

type hookClient struct {
	client.Client
	mu    sync.Mutex
	match func(client.Object) bool
	hook  func()
}

func (h *hookClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := h.Client.Get(ctx, key, obj, opts...)
	h.takeHook(obj)()
	return err
}

func (h *hookClient) takeHook(obj client.Object) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hook == nil || !h.match(obj) {
		return func() {}
	}
	hook := h.hook
	h.hook = nil
	return hook
}

func (h *hookClient) afterNextGet(match func(client.Object) bool, hook func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.match = match
	h.hook = hook
}

func isAuthRequest(obj client.Object) bool {
	_, ok := obj.(*v1alpha1.AuthRequest)
	return ok
}

func isCredential(obj client.Object) bool {
	_, ok := obj.(*v1alpha1.Credential)
	return ok
}

type upstreamCall struct {
	flow   methods.Flow
	answer methods.Answer
}

type fakeUpstream struct {
	mu        sync.Mutex
	subject   methods.Subject
	begins    []methods.Flow
	completes []upstreamCall
}

func (f *fakeUpstream) Name() string { return v1alpha1.MethodOIDC }

func (f *fakeUpstream) Kind() methods.Kind { return methods.Primary }

func (f *fakeUpstream) Begin(_ context.Context, flow methods.Flow, _ v1alpha1.User) (methods.Challenge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins = append(f.begins, flow)
	return methods.Challenge{Type: methods.ChallengeRedirect, Redirect: "https://dex.example.test/authorize"}, nil
}

func (f *fakeUpstream) Complete(_ context.Context, flow methods.Flow, _ v1alpha1.User, answer methods.Answer) (methods.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completes = append(f.completes, upstreamCall{flow: flow, answer: answer})
	subject := f.subject
	return methods.Result{Subject: &subject}, nil
}

func (f *fakeUpstream) Enroll(context.Context, v1alpha1.User, methods.Answer) (methods.Enrollment, error) {
	return methods.Enrollment{}, errors.New("the upstream method has no enrollment")
}

func (f *fakeUpstream) respondWith(subject methods.Subject) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subject = subject
}

func (f *fakeUpstream) calls() ([]methods.Flow, []upstreamCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]methods.Flow(nil), f.begins...), append([]upstreamCall(nil), f.completes...)
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

func testSettings() policy.Settings {
	return policy.Settings{SessionTTL: 12 * time.Hour, RefreshTTL: 720 * time.Hour, LockoutThreshold: 5, Host: "example.test", TLSMode: "SelfSigned"}
}

func newHarness(t *testing.T, limiter *methods.RateLimiter) harness {
	t.Helper()
	c := startTestEnv(t)
	settings := func(context.Context) (policy.Settings, error) { return testSettings(), nil }
	reader := &hookClient{Client: c}
	handlerClient := &hookClient{Client: c}
	st := store.New(store.Config{
		Client:   c,
		Reader:   reader,
		Random:   rand.Reader,
		Clock:    time.Now,
		Settings: settings,
		Keys:     func(context.Context) ([]keys.Key, error) { return nil, nil },
	})
	upstream := &fakeUpstream{}
	registry := methods.NewRegistry(
		methods.NewPassword(c, rand.Reader, methods.NewRateLimiter(100, time.Minute), settings),
		methods.NewTOTP(c, rand.Reader, func(context.Context) (string, error) { return "https://sso.example.test", nil }),
		methods.NewRecovery(c, rand.Reader),
		upstream,
	)
	server := httptest.NewTLSServer(Handler(Deps{
		Store:    st,
		Client:   handlerClient,
		Methods:  registry,
		Settings: settings,
		Random:   rand.Reader,
		Clock:    time.Now,
		Limiter:  limiter,
		Callback: func(_ context.Context, id string) string { return "/oauth/v2/authorize/callback?id=" + id },
	}))
	t.Cleanup(server.Close)
	create(t, c, &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Name: "bedrock-cli", Namespace: release.SystemNamespace},
		Spec: v1alpha1.OAuthClientSpec{
			ClientID:     "bedrock-cli",
			Public:       true,
			RedirectURIs: []string{"http://127.0.0.1/callback"},
			GrantTypes:   []string{v1alpha1.GrantAuthorizationCode, v1alpha1.GrantRefreshToken, v1alpha1.GrantDeviceCode},
		},
	})
	return harness{client: c, store: st, server: server, upstream: upstream, reader: reader, handler: handlerClient}
}

func create(t *testing.T, c client.Client, objects ...client.Object) {
	t.Helper()
	for _, obj := range objects {
		if err := c.Create(context.Background(), obj); err != nil {
			t.Fatalf("create %s: %v", obj.GetName(), err)
		}
	}
}

func createAuthRequest(t *testing.T, c client.Client) string {
	t.Helper()
	id, err := secret.FromAlphabet(rand.Reader, "abcdefghijklmnopqrstuvwxyz0123456789", 26)
	if err != nil {
		t.Fatal(err)
	}
	create(t, c, &v1alpha1.AuthRequest{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: release.SystemNamespace},
		Spec: v1alpha1.AuthRequestSpec{
			ClientID:            "bedrock-cli",
			RedirectURI:         "http://127.0.0.1:43123/callback",
			Scopes:              []string{"openid", "offline_access"},
			State:               "s1",
			ResponseType:        "code",
			CodeChallenge:       "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
			CodeChallengeMethod: "S256",
			ExpiresAt:           metav1.NewTime(time.Now().Add(30 * time.Minute)),
		},
	})
	return id
}

func createLocalUser(t *testing.T, c client.Client, username string) *v1alpha1.User {
	t.Helper()
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.UserObjectName(username), Namespace: release.SystemNamespace},
		Spec:       v1alpha1.UserSpec{Username: username, Methods: []string{v1alpha1.MethodPassword}},
	}
	create(t, c, user)
	if err := methods.SetPassword(context.Background(), c, rand.Reader, *user, testPassword); err != nil {
		t.Fatal(err)
	}
	return user
}

func storedRequest(t *testing.T, c client.Client, id string) v1alpha1.AuthRequest {
	t.Helper()
	var request v1alpha1.AuthRequest
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: release.SystemNamespace, Name: id}, &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func newBrowser(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport:     server.Client().Transport,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func send(t *testing.T, c *http.Client, method, target, csrf string, body any, header http.Header) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, target, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func get(t *testing.T, c *http.Client, target string) *http.Response {
	t.Helper()
	resp, err := c.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func challengeOf(t *testing.T, resp *http.Response) methods.Challenge {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var challenge methods.Challenge
	if err := json.Unmarshal(body, &challenge); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return challenge
}

func errorOf(t *testing.T, resp *http.Response, status int) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("status %d, want %d: %s", resp.StatusCode, status, body)
	}
	var answer struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return answer.Error
}

func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func startLogin(t *testing.T, h harness, browser *http.Client) (string, methods.Challenge) {
	t.Helper()
	id := createAuthRequest(t, h.client)
	resp := send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/start", "", map[string]string{"authRequest": id}, nil)
	return id, challengeOf(t, resp)
}

func answerWith(t *testing.T, h harness, browser *http.Client, csrf string, given methods.Answer) methods.Challenge {
	t.Helper()
	return challengeOf(t, send(t, browser, http.MethodPost, h.server.URL+"/api/v1/login/answer", csrf, given, nil))
}
