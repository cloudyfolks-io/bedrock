package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

const jwtShapedAlice = "jwt.shaped.alice"

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type serverFixture struct {
	front   *httptest.Server
	fake    *fakeAuthn
	prom    *fakePrometheus
	store   *Store
	subs    *Subresources
	logs    *syncBuffer
	visited []string
	http    *http.Client
}

func setPlatformHost(t *testing.T, value string) {
	t.Helper()
	ctx := context.Background()
	var setting v1alpha1.Setting
	err := testClient.Get(ctx, client.ObjectKey{Name: "platform.host"}, &setting)
	if apierrors.IsNotFound(err) {
		setting = v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "platform.host"}, Spec: v1alpha1.SettingSpec{Value: value}}
		if err := testClient.Create(ctx, &setting); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	setting.Spec.Value = value
	if err := testClient.Update(ctx, &setting); err != nil {
		t.Fatal(err)
	}
}

func seedServerRBAC(t *testing.T) {
	t.Helper()
	startEnv(t)
	subject := rbacv1.Subject{APIGroup: "rbac.authorization.k8s.io", Kind: "User", Name: "bedrock:alice"}
	createIfMissing(t,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "server-editor"},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"settings"}, Verbs: []string{"get", "list", "watch", "patch", "update"}},
				{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"virtualmachines/start"}, Verbs: []string{"update"}},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "server-editor"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "server-editor"},
			Subjects:   []rbacv1.Subject{subject},
		},
	)
	waitForAccess(t, jwtShapedAlice, authorizationv1.ResourceAttributes{Verb: "patch", Group: "bedrock.cloudyfolks.io", Resource: "settings"})
	waitForAccess(t, jwtShapedAlice, authorizationv1.ResourceAttributes{Verb: "update", Group: "bedrock.cloudyfolks.io", Resource: "virtualmachines", Subresource: "start", Namespace: "acme-web"})
}

func newServerFixture(t *testing.T, host string) *serverFixture {
	t.Helper()
	seedServerRBAC(t)
	setPlatformHost(t, host)
	createIfMissing(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ConsoleSecretName, Namespace: "bedrock-system"},
		Data:       map[string][]byte{"clientSecret": []byte(fakeClientSecret)},
	})
	logs := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	fake := newFakeAuthn(t)
	fx := &resolverFixture{fake: fake}
	fx.subject = "user-server-" + strings.ReplaceAll(t.Name(), "/", "-")
	user := &v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: strings.ToLower(fx.subject), Namespace: "bedrock-system"}, Spec: v1alpha1.UserSpec{Username: "alice"}}
	if err := testClient.Create(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	newAuthnSession(t, fake, 12*time.Hour)
	fake.mutate(func(f *fakeAuthn) {
		f.identity.Subject = user.Name
		f.accessToken = jwtShapedAlice
	})
	prom := newFakePrometheus(t)
	subs := NewSubresources()
	handler, err := New(ctxFor(t), Config{
		RestConfig: testCfg, Random: rand.Reader, Clock: time.Now,
		Panel:        fstest.MapFS{"index.html": {Data: []byte(`<div id="root"></div>`)}},
		Subresources: subs, AuthnAddress: fake.Address(), AuthnServerName: fakeAuthnServerName, AuthnCA: fake.caPEM,
		PrometheusURL: prom.url(t), PodName: "proxy-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(handler)
	t.Cleanup(front.Close)
	return &serverFixture{
		front: front, fake: fake, prom: prom, store: NewStore(testClient, time.Now), subs: subs, logs: logs,
		http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

func (fx *serverFixture) call(t *testing.T, method, host, target string, header http.Header, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, fx.front.URL+target, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	for name, values := range header {
		req.Header[name] = values
	}
	fx.visited = append(fx.visited, target)
	resp, err := fx.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (fx *serverFixture) signIn(t *testing.T, returnTo string) *http.Cookie {
	t.Helper()
	start := fx.call(t, http.MethodGet, "console.lab.example", "/oauth/login?returnTo="+url.QueryEscape(returnTo), nil, nil)
	location := start.Header.Get("Location")
	loginCookie := cookieNamed(start, CookieLogin)
	if start.StatusCode != http.StatusFound || loginCookie == nil {
		t.Fatalf("login start: %d %q", start.StatusCode, location)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	target := "/oauth/callback?code=" + url.QueryEscape(fx.fake.CodeFor(location)) + "&state=" + url.QueryEscape(parsed.Query().Get("state"))
	done := fx.call(t, http.MethodGet, "console.lab.example", target, http.Header{"Cookie": {loginCookie.String()}}, nil)
	session := cookieNamed(done, CookieSession)
	if done.StatusCode != http.StatusFound || done.Header.Get("Location") != returnTo || session == nil {
		t.Fatalf("callback: %d %q", done.StatusCode, done.Header.Get("Location"))
	}
	return session
}

func cookieHeader(cookie *http.Cookie, extra map[string]string) http.Header {
	header := http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}}
	for name, value := range extra {
		header.Set(name, value)
	}
	return header
}

func TestNewRoutesEveryHost(t *testing.T) {
	fx := newServerFixture(t, "lab.example")
	createIfMissing(t, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "server-route"}, Spec: v1alpha1.SettingSpec{Value: "1"}})
	settingPath := "/apis/bedrock.cloudyfolks.io/v1alpha1/settings/server-route"

	apex := fx.call(t, http.MethodGet, "lab.example", "/x?y=1", nil, nil)
	if apex.StatusCode != http.StatusTemporaryRedirect || apex.Header.Get("Location") != "https://console.lab.example/x?y=1" {
		t.Fatalf("apex: %d %q", apex.StatusCode, apex.Header.Get("Location"))
	}
	panel := fx.call(t, http.MethodGet, "console.lab.example", "/", nil, nil)
	if panel.StatusCode != http.StatusOK || !strings.Contains(bodyOf(t, panel), `id="root"`) {
		t.Fatalf("panel: %d", panel.StatusCode)
	}
	if login := fx.call(t, http.MethodGet, "console.lab.example", "/oauth/login", nil, nil); login.StatusCode != http.StatusFound {
		t.Fatalf("login: %d", login.StatusCode)
	}

	session := fx.signIn(t, "/admin/hosts")
	info := fx.call(t, http.MethodGet, "console.lab.example", PathSession, cookieHeader(session, nil), nil)
	infoBody := bodyOf(t, info)
	if info.StatusCode != http.StatusOK || !strings.Contains(infoBody, `"host":"lab.example"`) {
		t.Fatalf("session: %d %s", info.StatusCode, infoBody)
	}
	rec, err := fx.store.Session(context.Background(), secret.SHA256Hex(session.Value))
	if err != nil {
		t.Fatal(err)
	}

	read := fx.call(t, http.MethodGet, "console.lab.example", settingPath, cookieHeader(session, nil), nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("cookie GET: %d %s", read.StatusCode, bodyOf(t, read))
	}
	patch := func(headers map[string]string) *http.Response {
		headers["Content-Type"] = "application/merge-patch+json"
		headers["Origin"] = "https://console.lab.example"
		return fx.call(t, http.MethodPatch, "console.lab.example", settingPath, cookieHeader(session, headers), strings.NewReader(`{"spec":{"value":"2"}}`))
	}
	if resp := patch(map[string]string{}); resp.StatusCode != http.StatusForbidden || !strings.Contains(bodyOf(t, resp), `"csrf"`) {
		t.Fatalf("PATCH without CSRF: %d", resp.StatusCode)
	}
	if resp := patch(map[string]string{HeaderCSRF: rec.CSRF}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH with CSRF: %d %s", resp.StatusCode, bodyOf(t, resp))
	}

	bearer := http.Header{"Authorization": {"Bearer " + jwtShapedAlice}}
	if list := fx.call(t, http.MethodGet, "api.lab.example", "/apis/bedrock.cloudyfolks.io/v1alpha1/settings", bearer, nil); list.StatusCode != http.StatusOK {
		t.Fatalf("API host list: %d %s", list.StatusCode, bodyOf(t, list))
	}
	for _, path := range []string{"/oauth/login", PathSession, PathStream} {
		if resp := fx.call(t, http.MethodGet, "api.lab.example", path, nil, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("API host %s: %d", path, resp.StatusCode)
		}
	}
	if resp := fx.call(t, http.MethodGet, "api.lab.example", "/apis/bedrock.cloudyfolks.io/v1alpha1/settings", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("API host without bearer: %d", resp.StatusCode)
	}

	header := cookieHeader(session, map[string]string{"Origin": "https://console.lab.example", "Host": "console.lab.example"})
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(fx.front.URL, "http")+PathStream, header)
	if err != nil {
		t.Fatal(err)
	}
	expectFrame(t, conn, "hello")
	_ = conn.Close()

	if resp := fx.call(t, http.MethodPost, "console.lab.example", PathLogout, cookieHeader(session, map[string]string{"Origin": "https://console.lab.example"}), nil); resp.StatusCode != http.StatusForbidden || !strings.Contains(bodyOf(t, resp), `"csrf"`) {
		t.Fatalf("logout without CSRF: %d", resp.StatusCode)
	}
	if _, err := fx.store.Session(context.Background(), secret.SHA256Hex(session.Value)); err != nil {
		t.Fatalf("record gone after a refused logout: %v", err)
	}
	out := fx.call(t, http.MethodPost, "console.lab.example", PathLogout, cookieHeader(session, map[string]string{"Origin": "https://console.lab.example", HeaderCSRF: rec.CSRF}), nil)
	if out.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", out.StatusCode)
	}
	if _, err := fx.store.Session(context.Background(), secret.SHA256Hex(session.Value)); err == nil {
		t.Fatal("record survived the logout")
	}
}

func TestNoTokenInURLsOrLogs(t *testing.T) {
	fx := newServerFixture(t, "lab.example")
	session := fx.signIn(t, "/p/web")
	rec, err := fx.store.Session(context.Background(), secret.SHA256Hex(session.Value))
	if err != nil {
		t.Fatal(err)
	}
	fx.call(t, http.MethodGet, "console.lab.example", "/apis/bedrock.cloudyfolks.io/v1alpha1/settings", cookieHeader(session, nil), nil)
	fx.call(t, http.MethodGet, "api.lab.example", "/apis/bedrock.cloudyfolks.io/v1alpha1/settings", http.Header{"Authorization": {"Bearer " + jwtShapedAlice}}, nil)
	fx.call(t, http.MethodGet, "console.lab.example", PathSession, cookieHeader(session, nil), nil)
	sensitive := []string{rec.Access, rec.Refresh, session.Value, fakeClientSecret}
	urls := append(fx.fake.URIs(), fx.visited...)
	for _, seen := range append(urls, fx.logs.String()) {
		for _, value := range sensitive {
			if strings.Contains(seen, value) {
				t.Fatalf("%q leaks %q", seen, value)
			}
		}
	}
	for _, uri := range fx.fake.URIs() {
		if strings.Contains(uri, "access_token") || strings.Contains(uri, "refresh_token") {
			t.Fatalf("authn URL %q carries a token parameter", uri)
		}
	}
	for _, word := range []string{"Authorization", "Set-Cookie", "Bearer "} {
		if strings.Contains(fx.logs.String(), word) {
			t.Fatalf("logs hold %q: %s", word, fx.logs.String())
		}
	}
}

func TestReadyzDoesNotNeedThePlatformHost(t *testing.T) {
	fx := newServerFixture(t, "")
	defer setPlatformHost(t, "lab.example")
	ready := fx.call(t, http.MethodGet, "10.1.2.3:8443", "/readyz", nil, nil)
	if ready.StatusCode != http.StatusOK {
		t.Fatalf("readyz: %d", ready.StatusCode)
	}
	if health := fx.call(t, http.MethodGet, "10.1.2.3:8443", "/healthz", nil, nil); health.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %d", health.StatusCode)
	}
	console := fx.call(t, http.MethodGet, "console.lab.example", "/", nil, nil)
	if console.StatusCode != http.StatusServiceUnavailable || !strings.Contains(bodyOf(t, console), `"platform_host_not_set"`) {
		t.Fatalf("console: %d", console.StatusCode)
	}
}

func TestSubresourcesAreMounted(t *testing.T) {
	fx := newServerFixture(t, "lab.example")
	var mu sync.Mutex
	hits := 0
	err := fx.subs.Register("virtualmachines", "start", func(_ context.Context, _ client.Client, key client.ObjectKey, w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/vm1/start"
	session := fx.signIn(t, "/")
	rec, err := fx.store.Session(context.Background(), secret.SHA256Hex(session.Value))
	if err != nil {
		t.Fatal(err)
	}
	console := fx.call(t, http.MethodPost, "console.lab.example", path, cookieHeader(session, map[string]string{"Origin": "https://console.lab.example", HeaderCSRF: rec.CSRF}), nil)
	api := fx.call(t, http.MethodPost, "api.lab.example", path, http.Header{"Authorization": {"Bearer " + jwtShapedAlice}}, nil)
	if console.StatusCode != http.StatusAccepted || api.StatusCode != http.StatusAccepted || hits != 2 {
		t.Fatalf("console %d, api %d, hits %d", console.StatusCode, api.StatusCode, hits)
	}
}

func TestSweeperRunsAndStops(t *testing.T) {
	startEnv(t)
	store := NewStore(testClient, time.Now)
	hash := secret.SHA256Hex("sweeper-expired-login")
	if err := store.CreateLogin(context.Background(), hash, LoginState{ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Sweeper(ctx, store, 50*time.Millisecond) }()
	waitUntil(t, "the sweeper to delete the record", func() bool {
		var stored corev1.Secret
		err := testClient.Get(context.Background(), client.ObjectKey{Namespace: StoreNamespace, Name: "pl-" + hash}, &stored)
		return apierrors.IsNotFound(err)
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper did not stop")
	}
}
