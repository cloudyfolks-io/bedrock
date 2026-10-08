package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gorilla/websocket"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type flipSessions struct {
	mu      sync.Mutex
	entries map[string]Principal
	errs    map[string]error
}

func newFlipSessions() *flipSessions {
	return &flipSessions{entries: map[string]Principal{}, errs: map[string]error{}}
}

func (f *flipSessions) set(cookie string, principal Principal, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[cookie], f.errs[cookie] = principal, err
}

func (f *flipSessions) Resolve(_ context.Context, cookie string) (Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[cookie]; err != nil {
		return Principal{}, err
	}
	principal, found := f.entries[cookie]
	if !found {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}

func principalWithToken(subject, token string) Principal {
	return Principal{Subject: subject, Access: token, CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour)}
}

func newStreamServer(t *testing.T, u *Upstream, sessions SessionSource, opts StreamOptions) *httptest.Server {
	t.Helper()
	front, _ := newStreamServerHub(t, u, sessions, opts)
	return front
}

func newStreamServerHub(t *testing.T, u *Upstream, sessions SessionSource, opts StreamOptions) (*httptest.Server, *streamHub) {
	t.Helper()
	hub := newStreamHub(u, sessions, opts)
	chain := Authenticate(CookieAuth(sessions), RequireOrigin(labHost, http.HandlerFunc(hub.serve)))
	front := httptest.NewServer(Dispatch(labHost, Routes{Console: chain, API: http.NotFoundHandler(), Health: http.NotFoundHandler()}))
	t.Cleanup(front.Close)
	return front, hub
}

func dialStream(front *httptest.Server, origin, cookie string) (*websocket.Conn, *http.Response, error) {
	header := http.Header{"Host": {"console.lab.example"}}
	if origin != "" {
		header.Set("Origin", origin)
	}
	if cookie != "" {
		header.Set("Cookie", CookieSession+"="+cookie)
	}
	dialer := websocket.Dialer{Subprotocols: []string{StreamSubprotocol}}
	return dialer.Dial("ws"+strings.TrimPrefix(front.URL, "http")+PathStream, header)
}

const consoleOrigin = "https://console.lab.example"

func openStream(t *testing.T, front *httptest.Server, cookie string) *websocket.Conn {
	t.Helper()
	conn, _, err := dialStream(front, consoleOrigin, cookie)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func nextFrame(t *testing.T, conn *websocket.Conn) ServerFrame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var frame ServerFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return frame
}

func expectFrame(t *testing.T, conn *websocket.Conn, kind string) ServerFrame {
	t.Helper()
	frame := nextFrame(t, conn)
	if frame.Type != kind {
		t.Fatalf("frame %+v, want type %s", frame, kind)
	}
	return frame
}

func closeCode(t *testing.T, conn *websocket.Conn) int {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			var closed *websocket.CloseError
			if errors.As(err, &closed) {
				return closed.Code
			}
			t.Fatalf("connection ended without a close code: %v", err)
		}
	}
}

func send(t *testing.T, conn *websocket.Conn, frame ClientFrame) {
	t.Helper()
	if err := conn.WriteJSON(frame); err != nil {
		t.Fatal(err)
	}
}

func settingsFrame(id, rv string) ClientFrame {
	return ClientFrame{Op: "subscribe", ID: id, Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Resource: "settings", ResourceVersion: rv}
}

func watchLine(kind, name, rv string) string {
	return `{"type":"` + kind + `","object":{"apiVersion":"bedrock.cloudyfolks.io/v1alpha1","kind":"Setting","metadata":{"name":"` + name + `","resourceVersion":"` + rv + `","managedFields":[{"manager":"x"}]}}}` + "\n"
}

func flushed(w http.ResponseWriter, text string) {
	_, _ = w.Write([]byte(text))
	w.(http.Flusher).Flush()
}

func holdOpen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func watchRequests(api *fakeAPI) []seenRequest {
	var watches []seenRequest
	for _, seen := range api.seen() {
		if strings.Contains(seen.URI, "watch=true") {
			watches = append(watches, seen)
		}
	}
	return watches
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if condition() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitForAccess(t *testing.T, token string, attributes authorizationv1.ResourceAttributes) {
	t.Helper()
	reviews, err := kubernetes.NewForConfig(restConfigFor(token))
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "RBAC for "+token, func() bool {
		allowed, err := reviewAccess(context.Background(), reviews, attributes)
		return err == nil && allowed
	})
}

func seedStreamRBAC(t *testing.T) {
	t.Helper()
	startEnv(t)
	createIfMissing(t,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "stream-watcher"},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"settings", "projects"}, Verbs: []string{"list", "watch"}}},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "stream-watcher"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "stream-watcher"},
			Subjects:   []rbacv1.Subject{{APIGroup: "rbac.authorization.k8s.io", Kind: "User", Name: "bedrock:alice"}},
		},
	)
	waitForAccess(t, "token-alice", authorizationv1.ResourceAttributes{Verb: "watch", Group: "bedrock.cloudyfolks.io", Resource: "settings"})
}

func realStream(t *testing.T, opts StreamOptions) (*httptest.Server, *flipSessions) {
	t.Helper()
	seedStreamRBAC(t)
	u, err := NewUpstream(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	sessions := newFlipSessions()
	sessions.set("alice", principalWithToken("alice", "token-alice"), nil)
	sessions.set("bob", principalWithToken("bob", "token-bob"), nil)
	return newStreamServer(t, u, sessions, opts), sessions
}

func fakeStream(t *testing.T, handler http.HandlerFunc, opts StreamOptions) (*httptest.Server, *fakeAPI, *flipSessions) {
	t.Helper()
	front, api, sessions, _ := fakeStreamHub(t, handler, opts)
	return front, api, sessions
}

func fakeStreamHub(t *testing.T, handler http.HandlerFunc, opts StreamOptions) (*httptest.Server, *fakeAPI, *flipSessions, *streamHub) {
	t.Helper()
	api := newFakeAPI(t, handler)
	sessions := newFlipSessions()
	sessions.set("alice", principalWithToken("alice", "tok-1"), nil)
	sessions.set("bob", principalWithToken("bob", "tok-bob"), nil)
	front, hub := newStreamServerHub(t, api.upstream(t), sessions, opts)
	return front, api, sessions, hub
}

func newSetting(t *testing.T, name string) *v1alpha1.Setting {
	t.Helper()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.SettingSpec{Value: "1"}}
	if err := testClient.Create(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
	return setting
}

func TestStreamRefusesForeignOrigin(t *testing.T) {
	front, _ := realStream(t, StreamOptions{})
	for _, origin := range []string{"https://evil.test", ""} {
		_, resp, err := dialStream(front, origin, "alice")
		if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q: %v %v", origin, resp, err)
		}
	}
}

func TestStreamNeedsASession(t *testing.T) {
	front, _ := realStream(t, StreamOptions{})
	_, resp, err := dialStream(front, consoleOrigin, "")
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("%v %v", resp, err)
	}
}

func TestStreamSendsHelloFirst(t *testing.T) {
	front, _ := realStream(t, StreamOptions{})
	conn := openStream(t, front, "alice")
	hello := expectFrame(t, conn, "hello")
	if hello.Limits == nil || hello.Limits.MaxSubscriptions != 64 || hello.ExpiresAt == "" {
		t.Fatalf("hello %+v", hello)
	}
}

func TestStreamSubscribeAndEvent(t *testing.T) {
	front, _ := realStream(t, StreamOptions{})
	setting := newSetting(t, "stream-sub")
	var list v1alpha1.SettingList
	if err := testClient.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", list.ResourceVersion))
	if frame := expectFrame(t, conn, "subscribed"); frame.ID != "s1" {
		t.Fatalf("subscribed %+v", frame)
	}
	patch := client.MergeFrom(setting.DeepCopy())
	setting.Spec.Value = "2"
	if err := testClient.Patch(context.Background(), setting, patch); err != nil {
		t.Fatal(err)
	}
	event := expectFrame(t, conn, "event")
	var object map[string]any
	if err := json.Unmarshal(event.Object, &object); err != nil {
		t.Fatal(err)
	}
	meta := object["metadata"].(map[string]any)
	if event.ID != "s1" || event.Event != "MODIFIED" || meta["name"] != "stream-sub" || meta["resourceVersion"] != event.ResourceVersion || meta["managedFields"] != nil {
		t.Fatalf("event %+v object %v", event, object)
	}
}

func TestStreamForwardsBookmarks(t *testing.T) {
	front, _, _ := fakeStream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		flushed(w, `{"type":"BOOKMARK","object":{"apiVersion":"bedrock.cloudyfolks.io/v1alpha1","kind":"Setting","metadata":{"resourceVersion":"99"}}}`+"\n")
		<-r.Context().Done()
	}, StreamOptions{})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", "5"))
	expectFrame(t, conn, "subscribed")
	if bookmark := expectFrame(t, conn, "bookmark"); bookmark.ResourceVersion != "99" || bookmark.ID != "s1" {
		t.Fatalf("bookmark %+v", bookmark)
	}
}

func TestStreamRefusesOtherGroups(t *testing.T) {
	front, api, _ := fakeStream(t, holdOpen, StreamOptions{})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	for i, group := range [][2]string{{"", "secrets"}, {"apps", "deployments"}, {"kubevirt.io", "virtualmachines"}} {
		frame := settingsFrame("g"+strconv.Itoa(i), "1")
		frame.Group, frame.Resource = group[0], group[1]
		send(t, conn, frame)
		if answer := expectFrame(t, conn, "error"); answer.Code != "resource_not_allowed" {
			t.Fatalf("answer %+v", answer)
		}
	}
	send(t, conn, ClientFrame{Op: "ping"})
	expectFrame(t, conn, "pong")
	if len(watchRequests(api)) != 0 {
		t.Fatal("a refused subscription reached the API server")
	}
}

func TestStreamForbiddenSubscription(t *testing.T) {
	front, _ := realStream(t, StreamOptions{})
	conn := openStream(t, front, "bob")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", "1"))
	if answer := expectFrame(t, conn, "error"); answer.Code != "forbidden" || answer.ID != "s1" {
		t.Fatalf("answer %+v", answer)
	}
}

func TestStreamRejectsBadFrames(t *testing.T) {
	front, _, _ := fakeStream(t, holdOpen, StreamOptions{})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	expectError := func(frame ClientFrame, code string) {
		t.Helper()
		send(t, conn, frame)
		if answer := expectFrame(t, conn, "error"); answer.Code != code {
			t.Fatalf("frame %+v: answer %+v, want %s", frame, answer, code)
		}
	}
	expectError(settingsFrame("e1", ""), "invalid")
	expectError(settingsFrame("with space", "1"), "invalid")
	expectError(settingsFrame(strings.Repeat("a", 65), "1"), "invalid")
	send(t, conn, settingsFrame("first", "1"))
	expectFrame(t, conn, "subscribed")
	expectError(settingsFrame("first", "1"), "duplicate_id")
	expectError(settingsFrame("second", "1"), "duplicate_subscription")
	for i := 1; i < 64; i++ {
		frame := settingsFrame("n"+strconv.Itoa(i), "1")
		frame.LabelSelector = "k=" + strconv.Itoa(i)
		send(t, conn, frame)
		expectFrame(t, conn, "subscribed")
	}
	over := settingsFrame("n64", "1")
	over.LabelSelector = "k=64"
	expectError(over, "limit_exceeded")
}

func TestStreamProtocolViolationClosesWith4400(t *testing.T) {
	front, _, _ := fakeStream(t, holdOpen, StreamOptions{})
	payloads := map[string][]byte{
		"malformed JSON": []byte("{not json"),
		"unknown op":     []byte(`{"op":"explode"}`),
		"large frame":    []byte(`{"op":"ping","id":"` + strings.Repeat("a", 20<<10) + `"}`),
	}
	for name, payload := range payloads {
		conn := openStream(t, front, "alice")
		expectFrame(t, conn, "hello")
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatal(err)
		}
		if code := closeCode(t, conn); code != CloseProtocol {
			t.Errorf("%s: close code %d", name, code)
		}
	}
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, conn); code != CloseProtocol {
		t.Errorf("binary frame: close code %d", code)
	}
}

func TestStreamUnsubscribeStopsTheWatch(t *testing.T) {
	ended := make(chan struct{})
	var once sync.Once
	front, _, _ := fakeStream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		once.Do(func() { close(ended) })
	}, StreamOptions{})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", "1"))
	expectFrame(t, conn, "subscribed")
	send(t, conn, ClientFrame{Op: "unsubscribe", ID: "s1"})
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream watch did not end")
	}
	send(t, conn, settingsFrame("s1", "1"))
	expectFrame(t, conn, "subscribed")
}

func TestStreamLimitsConnectionsPerUser(t *testing.T) {
	front, _, _ := fakeStream(t, holdOpen, StreamOptions{})
	conns := make([]*websocket.Conn, 0, 8)
	for i := 0; i < 8; i++ {
		conn := openStream(t, front, "alice")
		expectFrame(t, conn, "hello")
		conns = append(conns, conn)
	}
	_, resp, err := dialStream(front, consoleOrigin, "alice")
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("ninth connection: %v %v", resp, err)
	}
	other := openStream(t, front, "bob")
	expectFrame(t, other, "hello")
	_ = conns[0].Close()
	waitUntil(t, "a free connection slot", func() bool {
		conn, _, err := dialStream(front, consoleOrigin, "alice")
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
}

func TestStreamPingPong(t *testing.T) {
	front, _, _ := fakeStream(t, holdOpen, StreamOptions{PingInterval: 50 * time.Millisecond})
	conn := openStream(t, front, "alice")
	pings := make(chan struct{}, 8)
	conn.SetPingHandler(func(string) error {
		select {
		case pings <- struct{}{}:
		default:
		}
		return nil
	})
	expectFrame(t, conn, "hello")
	send(t, conn, ClientFrame{Op: "ping"})
	expectFrame(t, conn, "pong")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	select {
	case <-pings:
	case <-time.After(5 * time.Second):
		t.Fatal("the server sent no ping")
	}
}

func TestStreamRefusesVersionsThatEscapeTheGroup(t *testing.T) {
	front, api, _ := fakeStream(t, holdOpen, StreamOptions{})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	for i, version := range []string{"..", ".", "a..b", "v1/x", "V1", "-v1", "v1alpha1/../x"} {
		frame := settingsFrame("v"+strconv.Itoa(i), "1")
		frame.Version = version
		send(t, conn, frame)
		if answer := expectFrame(t, conn, "error"); answer.Code != "invalid" {
			t.Fatalf("version %q: %+v", version, answer)
		}
	}
	frame := settingsFrame("g", "1")
	frame.Group = "bedrock.cloudyfolks.io/../x"
	send(t, conn, frame)
	if answer := expectFrame(t, conn, "error"); answer.Code != "resource_not_allowed" {
		t.Fatalf("group: %+v", answer)
	}
	if len(watchRequests(api)) != 0 {
		t.Fatal("a refused subscription reached the API server")
	}
}

func TestStreamEvictsWhenQueuedBytesPassTheLimit(t *testing.T) {
	hub := newStreamHub(nil, nil, StreamOptions{SendBytes: 1000})
	conn := &streamConn{hub: hub, out: make(chan ServerFrame, 100), closing: make(chan closeRequest, 1)}
	small := ServerFrame{Type: "event", Object: make([]byte, 300)}
	for i := 0; i < 2; i++ {
		conn.enqueue(small)
	}
	select {
	case request := <-conn.closing:
		t.Fatalf("closed too early: %+v", request)
	default:
	}
	for i := 0; i < 5; i++ {
		conn.enqueue(small)
	}
	select {
	case request := <-conn.closing:
		if request.code != CloseOverloaded {
			t.Fatalf("close code %d", request.code)
		}
	default:
		t.Fatal("the byte limit did not evict the client")
	}
	if hub.overloads.Load() == 0 {
		t.Fatal("no overload was counted")
	}
	huge := &streamConn{hub: hub, out: make(chan ServerFrame, 100), closing: make(chan closeRequest, 1)}
	huge.enqueue(ServerFrame{Type: "event", Object: make([]byte, 5000)})
	select {
	case request := <-huge.closing:
		t.Fatalf("a single frame on an empty queue must pass: %+v", request)
	default:
	}
}

func TestStreamCapsAllConnections(t *testing.T) {
	front, _, sessions := fakeStream(t, holdOpen, StreamOptions{MaxConnections: 2})
	sessions.set("carol", principalWithToken("carol", "tok-c"), nil)
	first := openStream(t, front, "alice")
	expectFrame(t, first, "hello")
	second := openStream(t, front, "bob")
	expectFrame(t, second, "hello")
	_, resp, err := dialStream(front, consoleOrigin, "carol")
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("third connection: %v %v", resp, err)
	}
	_ = first.Close()
	waitUntil(t, "a free slot", func() bool {
		conn, _, err := dialStream(front, consoleOrigin, "carol")
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
}
