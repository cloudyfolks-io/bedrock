package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	authorizationv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type reviewAPI struct {
	*fakeAPI
	mu       sync.Mutex
	access   []authorizationv1.ResourceAttributes
	tokens   []string
	whoami   int
	allow    func(authorizationv1.ResourceAttributes) bool
	username string
}

func newReviewAPI(t *testing.T, allow func(authorizationv1.ResourceAttributes) bool) *reviewAPI {
	t.Helper()
	review := &reviewAPI{allow: allow, username: "bedrock:alice"}
	review.fakeAPI = newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/selfsubjectaccessreviews"):
			body, _ := io.ReadAll(r.Body)
			var parsed authorizationv1.SelfSubjectAccessReview
			_ = json.Unmarshal(body, &parsed)
			review.mu.Lock()
			review.access = append(review.access, *parsed.Spec.ResourceAttributes)
			review.tokens = append(review.tokens, r.Header.Get("Authorization"))
			review.mu.Unlock()
			allowed := review.allow(*parsed.Spec.ResourceAttributes)
			_, _ = w.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectAccessReview","status":{"allowed":` + boolText(allowed) + `}}`))
		case strings.HasSuffix(r.URL.Path, "/selfsubjectreviews"):
			review.mu.Lock()
			review.whoami++
			review.mu.Unlock()
			_, _ = w.Write([]byte(`{"apiVersion":"authentication.k8s.io/v1","kind":"SelfSubjectReview","status":{"userInfo":{"username":"` + review.username + `","groups":["bedrock:ops"]}}}`))
		default:
			http.NotFound(w, r)
		}
	})
	return review
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func (r *reviewAPI) reviews() []authorizationv1.ResourceAttributes {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]authorizationv1.ResourceAttributes{}, r.access...)
}

type calledHandler struct {
	mu   sync.Mutex
	keys []client.ObjectKey
	cs   []client.Client
	user User
}

func (c *calledHandler) handler(status int) SubresourceHandler {
	return func(_ context.Context, cl client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.keys = append(c.keys, key)
		c.cs = append(c.cs, cl)
		c.user, _ = UserFromContext(r.Context())
		c.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte("from handler"))
	}
}

func (c *calledHandler) calls() []client.ObjectKey {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]client.ObjectKey{}, c.keys...)
}

func routerFixture(t *testing.T, allow func(authorizationv1.ResourceAttributes) bool) (*Subresources, *reviewAPI, *calledHandler, client.Client, http.Handler, *bool) {
	t.Helper()
	subs := NewSubresources()
	api := newReviewAPI(t, allow)
	called := &calledHandler{}
	for _, pair := range [][2]string{
		{"virtualmachines", "start"}, {"virtualmachines", "snapshot"}, {"virtualmachines", "console/vnc"},
		{"images", "upload"}, {"clusterimages", "upload"},
	} {
		if err := subs.Register(pair[0], pair[1], called.handler(http.StatusOK)); err != nil {
			t.Fatal(err)
		}
	}
	privileged := fakeReader(t)
	reachedNext := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reachedNext = true
		w.WriteHeader(http.StatusTeapot)
	})
	router := SubresourceRouter(subs, api.upstream(t), privileged, next)
	return subs, api, called, privileged, router, &reachedNext
}

func allowAll(authorizationv1.ResourceAttributes) bool { return true }

const vmStart = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/vm1/start"

func TestRegisteredSubresourceRunsAfterAuthorization(t *testing.T) {
	_, api, called, privileged, router, _ := routerFixture(t, allowAll)
	handler := asCaller(router, "caller-token")
	rec := do(handler, httptest.NewRequest(http.MethodPost, vmStart, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "from handler" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	keys := called.calls()
	if len(keys) != 1 || keys[0] != (client.ObjectKey{Namespace: "acme-web", Name: "vm1"}) || called.cs[0] != privileged {
		t.Fatalf("handler saw %v", keys)
	}
	want := authorizationv1.ResourceAttributes{Namespace: "acme-web", Verb: "update", Group: "bedrock.cloudyfolks.io", Resource: "virtualmachines", Subresource: "start", Name: "vm1"}
	if got := api.reviews(); len(got) != 1 || got[0] != want || api.tokens[0] != "Bearer caller-token" {
		t.Fatalf("review %+v token %v", got, api.tokens)
	}

	_, denyAPI, denyCalled, _, denyRouter, _ := routerFixture(t, func(authorizationv1.ResourceAttributes) bool { return false })
	rec = do(asCaller(denyRouter, "caller-token"), httptest.NewRequest(http.MethodPost, vmStart, nil))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"forbidden"`) || len(denyCalled.calls()) != 0 || len(denyAPI.reviews()) != 1 {
		t.Fatalf("denied: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSubresourceVerbFollowsTheTable(t *testing.T) {
	cases := []struct{ plural, sub, method, want string }{
		{"virtualmachines", "start", "POST", "update"},
		{"virtualmachines", "stop", "POST", "update"},
		{"virtualmachines", "restart", "POST", "update"},
		{"virtualmachines", "migrate", "POST", "update"},
		{"virtualmachines", "snapshot", "POST", "update"},
		{"virtualmachines", "snapshot", "GET", "get"},
		{"virtualmachines", "console/vnc", "GET", "get"},
		{"virtualmachines", "console/serial", "GET", "get"},
		{"images", "upload", "POST", "create"},
		{"volumes", "x", "GET", "get"},
		{"volumes", "x", "POST", "update"},
		{"volumes", "x", "PUT", "update"},
		{"volumes", "x", "PATCH", "update"},
		{"volumes", "x", "DELETE", "delete"},
	}
	for _, tc := range cases {
		if got := SubresourceVerb(tc.plural, tc.sub, tc.method); got != tc.want {
			t.Errorf("SubresourceVerb(%s, %s, %s) = %s, want %s", tc.plural, tc.sub, tc.method, got, tc.want)
		}
	}
	_, _, called, _, router, reachedNext := routerFixture(t, allowAll)
	for _, method := range []string{http.MethodHead, http.MethodOptions} {
		*reachedNext = false
		do(asCaller(router, "caller-token"), httptest.NewRequest(method, vmStart, nil))
		if !*reachedNext || len(called.calls()) != 0 {
			t.Errorf("%s did not fall through", method)
		}
	}
}

func TestUnregisteredSubresourceFallsThrough(t *testing.T) {
	_, _, called, _, router, reachedNext := routerFixture(t, allowAll)
	for _, tail := range []string{"status", "unknown"} {
		*reachedNext = false
		do(asCaller(router, "caller-token"), httptest.NewRequest(http.MethodPost, "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/vm1/"+tail, nil))
		if !*reachedNext {
			t.Errorf("%s did not reach next", tail)
		}
	}
	if len(called.calls()) != 0 {
		t.Fatal("a handler ran")
	}
}

func TestClusterScopedSubresource(t *testing.T) {
	_, _, called, _, router, _ := routerFixture(t, allowAll)
	rec := do(asCaller(router, "caller-token"), httptest.NewRequest(http.MethodPost, "/apis/bedrock.cloudyfolks.io/v1alpha1/clusterimages/ubuntu/upload", nil))
	keys := called.calls()
	if rec.Code != http.StatusOK || len(keys) != 1 || keys[0] != (client.ObjectKey{Name: "ubuntu"}) {
		t.Fatalf("%d %v", rec.Code, keys)
	}
}

func TestMultiSegmentSubresource(t *testing.T) {
	_, api, called, _, router, reachedNext := routerFixture(t, allowAll)
	base := "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/vm1/"
	if rec := do(asCaller(router, "caller-token"), httptest.NewRequest(http.MethodGet, base+"console/vnc", nil)); rec.Code != http.StatusOK {
		t.Fatalf("console/vnc: %d", rec.Code)
	}
	if got := api.reviews(); len(got) != 1 || got[0].Subresource != "console/vnc" || got[0].Verb != "get" {
		t.Fatalf("review %+v", got)
	}
	for _, tail := range []string{"console/vnc/extra", "console/../x"} {
		*reachedNext = false
		do(asCaller(router, "caller-token"), httptest.NewRequest(http.MethodGet, base+tail, nil))
		if !*reachedNext {
			t.Errorf("%s did not fall through", tail)
		}
	}
	if len(called.calls()) != 1 {
		t.Fatalf("handler calls %d", len(called.calls()))
	}
}

func TestSubresourcePathIsNotTraversable(t *testing.T) {
	_, _, called, _, router, reachedNext := routerFixture(t, allowAll)
	base := "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/vm1"
	for _, target := range []string{
		base + "%2fstart", base + "/../vm1/start", base + "//start", base + "/start/",
		"/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces//virtualmachines/vm1/start", "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces",
	} {
		*reachedNext = false
		do(asCaller(router, "caller-token"), httptest.NewRequest(http.MethodPost, target, nil))
		if !*reachedNext {
			t.Errorf("%s did not fall through", target)
		}
	}
	if len(called.calls()) != 0 {
		t.Fatal("a handler ran")
	}
}

func TestPluralIsTheRegistryKey(t *testing.T) {
	_, api, called, _, router, reachedNext := routerFixture(t, allowAll)
	for _, plural := range []string{"VirtualMachines", "virtualmachine", "widgets"} {
		*reachedNext = false
		do(asCaller(router, "caller-token"), httptest.NewRequest(http.MethodPost, "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/"+plural+"/vm1/start", nil))
		if !*reachedNext {
			t.Errorf("%s did not fall through", plural)
		}
	}
	if len(called.calls()) != 0 || len(api.seen()) != 0 {
		t.Fatalf("handler calls %d, API requests %d", len(called.calls()), len(api.seen()))
	}
	do(asCaller(router, "caller-token"), httptest.NewRequest(http.MethodPost, "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/images/ubuntu/upload", nil))
	for _, seen := range api.seen() {
		if !strings.Contains(seen.URI, "/selfsubject") {
			t.Fatalf("unexpected API call %s", seen.URI)
		}
	}
}

func TestHandlerCanHijack(t *testing.T) {
	subs := NewSubresources()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	err := subs.Register("virtualmachines", "console/vnc", func(_ context.Context, _ client.Client, _ client.ObjectKey, w http.ResponseWriter, r *http.Request) {
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
	if err != nil {
		t.Fatal(err)
	}
	api := newReviewAPI(t, allowAll)
	router := SubresourceRouter(subs, api.upstream(t), fakeReader(t), http.NotFoundHandler())
	console := Authenticate(CookieAuth(aliceSessions()), RequireOrigin(labHost, router))
	front := httptest.NewServer(Dispatch(labHost, Routes{Console: console, API: http.NotFoundHandler(), Health: http.NotFoundHandler()}))
	t.Cleanup(front.Close)
	header := http.Header{"Origin": {"https://console.lab.example"}, "Host": {"console.lab.example"}, "Cookie": {CookieSession + "=good"}}
	url := "ws" + strings.TrimPrefix(front.URL, "http") + "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/vm1/console/vnc"
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("RFB")); err != nil {
		t.Fatal(err)
	}
	_, echoed, err := conn.ReadMessage()
	if err != nil || string(echoed) != "RFB" {
		t.Fatalf("echo %q, %v", echoed, err)
	}
}

func TestHandlerReceivesCallerIdentity(t *testing.T) {
	_, api, called, _, router, _ := routerFixture(t, allowAll)
	cookieCaller := Caller{Token: "cookie-access", Principal: &Principal{Subject: "alice", Groups: []string{"ops"}}}
	req := httptest.NewRequest(http.MethodPost, vmStart, nil)
	do(router, req.WithContext(WithCaller(req.Context(), cookieCaller)))
	if called.user.Name != "bedrock:alice" || len(called.user.Groups) != 1 || called.user.Groups[0] != "bedrock:ops" || api.whoami != 0 {
		t.Fatalf("cookie caller: %+v, %d reviews", called.user, api.whoami)
	}
	bearer := asCaller(router, "bearer-token")
	for i := 0; i < 3; i++ {
		do(bearer, httptest.NewRequest(http.MethodPost, vmStart, nil))
	}
	if called.user.Name != "bedrock:alice" || api.whoami != 1 {
		t.Fatalf("bearer caller: %+v, %d reviews", called.user, api.whoami)
	}
}

func TestHandlerErrorsAreNotRewritten(t *testing.T) {
	subs := NewSubresources()
	for _, status := range []int{http.StatusNotFound, http.StatusConflict} {
		name := "stop"
		if status == http.StatusConflict {
			name = "restart"
		}
		if err := subs.Register("virtualmachines", name, (&calledHandler{}).handler(status)); err != nil {
			t.Fatal(err)
		}
	}
	api := newReviewAPI(t, allowAll)
	router := asCaller(SubresourceRouter(subs, api.upstream(t), fakeReader(t), http.NotFoundHandler()), "caller-token")
	base := "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/vm1/"
	if rec := do(router, httptest.NewRequest(http.MethodPost, base+"stop", nil)); rec.Code != http.StatusNotFound || rec.Body.String() != "from handler" {
		t.Errorf("stop: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(router, httptest.NewRequest(http.MethodPost, base+"restart", nil)); rec.Code != http.StatusConflict || rec.Body.String() != "from handler" {
		t.Errorf("restart: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandlerSeesOneIdentityFormOnBothPaths(t *testing.T) {
	_, _, called, _, router, _ := routerFixture(t, allowAll)
	cookieCaller := Caller{Token: "cookie-access", Principal: &Principal{Subject: "alice", Groups: []string{"ops"}}}
	req := httptest.NewRequest(http.MethodPost, vmStart, nil)
	do(router, req.WithContext(WithCaller(req.Context(), cookieCaller)))
	cookieUser := called.user
	do(asCaller(router, "bearer-token"), httptest.NewRequest(http.MethodPost, vmStart, nil))
	bearerUser := called.user
	if cookieUser.Name != bearerUser.Name || strings.Join(cookieUser.Groups, ",") != strings.Join(bearerUser.Groups, ",") {
		t.Fatalf("cookie %+v, bearer %+v", cookieUser, bearerUser)
	}
}
