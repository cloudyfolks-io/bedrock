package container

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func podNamed(name string, ready bool) corev1.Pod {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	return pod
}

func names(pods []corev1.Pod) []string {
	out := make([]string, len(pods))
	for i, pod := range pods {
		out[i] = pod.Name
	}
	return out
}

func TestPickPods(t *testing.T) {
	pods := []corev1.Pod{podNamed("a", true), podNamed("b", false)}
	got, err := PickPods(pods, "")
	if err != nil || !reflect.DeepEqual(names(got), []string{"a", "b"}) {
		t.Fatalf("all pods: %v %v", names(got), err)
	}
	got, err = PickPods(pods, "b")
	if err != nil || !reflect.DeepEqual(names(got), []string{"b"}) {
		t.Fatalf("one pod: %v %v", names(got), err)
	}
	_, err = PickPods(pods, "stranger")
	if actionErr, ok := err.(ActionError); !ok || actionErr.Code != http.StatusNotFound {
		t.Fatalf("a pod of another container is a 404: %v", err)
	}
	_, err = PickPods(nil, "")
	if actionErr, ok := err.(ActionError); !ok || actionErr.Code != http.StatusConflict {
		t.Fatalf("no pod is a 409: %v", err)
	}
}

func TestPickReadyPod(t *testing.T) {
	pods := []corev1.Pod{podNamed("a", false), podNamed("b", true), podNamed("c", true)}
	pod, err := PickReadyPod(pods, "")
	if err != nil || pod.Name != "b" {
		t.Fatalf("the first ready pod: %v %v", pod.Name, err)
	}
	pod, err = PickReadyPod(pods, "c")
	if err != nil || pod.Name != "c" {
		t.Fatalf("the named pod: %v %v", pod.Name, err)
	}
	if _, err = PickReadyPod(pods, "a"); err == nil || err.(ActionError).Code != http.StatusConflict {
		t.Fatalf("a pod that is not ready is a 409: %v", err)
	}
	if _, err = PickReadyPod(pods, "z"); err == nil || err.(ActionError).Code != http.StatusNotFound {
		t.Fatalf("an unknown pod is a 404: %v", err)
	}
	terminating := podNamed("d", true)
	terminating.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	if _, err = PickReadyPod([]corev1.Pod{terminating}, ""); err == nil {
		t.Fatal("a terminating pod is not ready")
	}
}

func TestParseLogQuery(t *testing.T) {
	query, err := ParseLogQuery(url.Values{})
	if err != nil || query.Tail != 200 || query.Follow || query.Previous || query.Timestamps || query.Pod != "" {
		t.Fatalf("defaults %+v %v", query, err)
	}
	query, err = ParseLogQuery(url.Values{"pod": {"web-1"}, "tail": {"10"}, "follow": {"true"}, "previous": {"true"}, "timestamps": {"true"}})
	if err != nil || query.Pod != "web-1" || query.Tail != 10 || !query.Follow || !query.Previous || !query.Timestamps {
		t.Fatalf("values %+v %v", query, err)
	}
	for _, values := range []url.Values{{"tail": {"0"}}, {"tail": {"10001"}}, {"tail": {"x"}}, {"follow": {"maybe"}}, {"previous": {"2"}}, {"timestamps": {"yes please"}}} {
		if _, err := ParseLogQuery(values); err == nil || err.(ActionError).Code != http.StatusBadRequest {
			t.Errorf("%v must be refused: %v", values, err)
		}
	}
	for _, tail := range []string{"1", "10000"} {
		if _, err := ParseLogQuery(url.Values{"tail": {tail}}); err != nil {
			t.Errorf("tail %s must pass: %v", tail, err)
		}
	}
}

func TestMergeLogsPrefixesEveryLineWithItsPod(t *testing.T) {
	var out strings.Builder
	err := MergeLogs(&out, []NamedStream{
		{Pod: "web-a", Stream: strings.NewReader("a1\na2\n")},
		{Pod: "web-b", Stream: strings.NewReader("b1\nb2 without newline")},
		{Pod: "web-c", Stream: strings.NewReader("")},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	slices.Sort(lines)
	want := []string{"[web-a] a1", "[web-a] a2", "[web-b] b1", "[web-b] b2 without newline"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines %q", lines)
	}
}

func TestMergeLogsKeepsLinesWholeAndInOrderPerPod(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 500; i++ {
		a.WriteString("alpha line\n")
		b.WriteString("beta line\n")
	}
	var out strings.Builder
	if err := MergeLogs(&out, []NamedStream{{Pod: "a", Stream: strings.NewReader(a.String())}, {Pod: "b", Stream: strings.NewReader(b.String())}}); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		counts[line]++
	}
	if counts["[a] alpha line"] != 500 || counts["[b] beta line"] != 500 || len(counts) != 2 {
		t.Fatalf("counts %v", counts)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestMergeLogsReportsAWriteFailure(t *testing.T) {
	if err := MergeLogs(failingWriter{}, []NamedStream{{Pod: "a", Stream: strings.NewReader("x\n")}}); err == nil {
		t.Fatal("a failed write must be reported")
	}
}

func TestParseExecQuery(t *testing.T) {
	query, err := ParseExecQuery(url.Values{})
	if err != nil || !reflect.DeepEqual(query.Command, []string{"/bin/sh"}) || query.TTY || query.Pod != "" {
		t.Fatalf("defaults %+v %v", query, err)
	}
	query, err = ParseExecQuery(url.Values{"pod": {"web-1"}, "command": {"ls", "-la"}, "tty": {"true"}})
	if err != nil || query.Pod != "web-1" || !reflect.DeepEqual(query.Command, []string{"ls", "-la"}) || !query.TTY {
		t.Fatalf("values %+v %v", query, err)
	}
	for _, values := range []url.Values{{"tty": {"maybe"}}, {"command": {"ls", ""}}} {
		if _, err := ParseExecQuery(values); err == nil {
			t.Errorf("%v must be refused", values)
		}
	}
}

func TestExecTargetPointsAtThePodExec(t *testing.T) {
	target, err := ExecTarget(&rest.Config{Host: "https://api.example.test:6443"}, "acme-web", "web-1-a", ExecQuery{Command: []string{"ls", "-la"}, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	if target.Scheme != "https" || target.Host != "api.example.test:6443" || target.Path != "/api/v1/namespaces/acme-web/pods/web-1-a/exec" {
		t.Fatalf("target %v", target)
	}
	values := target.Query()
	if values.Get("container") != "app" || values.Get("tty") != "true" || values.Get("stderr") != "false" || values.Get("stdin") != "true" || values.Get("stdout") != "true" || !reflect.DeepEqual(values["command"], []string{"ls", "-la"}) {
		t.Fatalf("query %v", values)
	}
	plain, _ := ExecTarget(&rest.Config{Host: "https://api.example.test"}, "n", "p", ExecQuery{Command: []string{"/bin/sh"}})
	if plain.Query().Get("stderr") != "true" || plain.Query().Get("tty") != "false" {
		t.Fatalf("query %v", plain.Query())
	}
}

type fakeAPI struct {
	mu      sync.Mutex
	paths   []string
	queries []url.Values
	headers []http.Header
	logs    map[string]string
	Server  *httptest.Server
	Done    chan struct{}
}

func newFakeAPI(t *testing.T, logs map[string]string) *fakeAPI {
	t.Helper()
	fake := &fakeAPI{logs: logs, Done: make(chan struct{}, 8)}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.paths = append(fake.paths, r.URL.Path)
		fake.queries = append(fake.queries, r.URL.Query())
		fake.headers = append(fake.headers, r.Header.Clone())
		fake.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/log") {
			parts := strings.Split(r.URL.Path, "/")
			body, found := fake.logs[parts[len(parts)-2]]
			if !found {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, body)
			return
		}
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Protocol: " + r.Header.Get("Sec-WebSocket-Protocol") + "\r\n\r\n")
		_ = buffered.Flush()
		_, _ = io.Copy(conn, buffered)
		fake.Done <- struct{}{}
	}))
	t.Cleanup(fake.Server.Close)
	return fake
}

func (f *fakeAPI) seen() ([]string, []url.Values, []http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.paths), slices.Clone(f.queries), slices.Clone(f.headers)
}

func (f *fakeAPI) deps() Deps {
	return Deps{Config: &rest.Config{Host: f.Server.URL, BearerToken: "proxy-token"}, Now: func() time.Time { return fixedNow }}
}

func seedReadyPod(t *testing.T, c client.Client, namespace, name, container string, ready bool) {
	t.Helper()
	pod := createPod(t, c, namespace, name, container)
	if ready {
		updateStatus(t, c, &pod, func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		})
	}
}

func serveHandler(t *testing.T, handler func(context.Context, client.Client, client.ObjectKey, http.ResponseWriter, *http.Request), c client.Client, key client.ObjectKey) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(context.Background(), c, key, w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	must(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	must(t, err)
	return resp.StatusCode, string(body)
}

func TestLogsOfOnePodAreRaw(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", noMutation)
	createContainer(t, c, ns, "other", noMutation)
	seedReadyPod(t, c, ns, "web-1-a", "web", true)
	seedReadyPod(t, c, ns, "other-1-a", "other", true)
	api := newFakeAPI(t, map[string]string{"web-1-a": "hello\nworld\n", "other-1-a": "secret\n"})
	server := serveHandler(t, Logs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "web"})
	code, body := get(t, server.URL+"/?tail=5&timestamps=true&previous=true")
	if code != http.StatusOK || body != "hello\nworld\n" {
		t.Fatalf("%d %q", code, body)
	}
	paths, queries, _ := api.seen()
	if len(paths) != 1 || paths[0] != "/api/v1/namespaces/"+ns+"/pods/web-1-a/log" {
		t.Fatalf("paths %v", paths)
	}
	q := queries[0]
	if q.Get("container") != "app" || q.Get("tailLines") != "5" || q.Get("timestamps") != "true" || q.Get("previous") != "true" {
		t.Fatalf("query %v", q)
	}
}

func TestLogsOfSeveralPodsAreMergedWithPrefixes(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", noMutation)
	seedReadyPod(t, c, ns, "web-1-a", "web", true)
	seedReadyPod(t, c, ns, "web-1-b", "web", true)
	api := newFakeAPI(t, map[string]string{"web-1-a": "a1\n", "web-1-b": "b1\n"})
	server := serveHandler(t, Logs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "web"})
	code, body := get(t, server.URL+"/")
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	slices.Sort(lines)
	if code != http.StatusOK || !reflect.DeepEqual(lines, []string{"[web-1-a] a1", "[web-1-b] b1"}) {
		t.Fatalf("%d %q", code, body)
	}
	_, queries, _ := api.seen()
	for _, q := range queries {
		if q.Get("tailLines") != "200" {
			t.Fatalf("the default tail is 200: %v", q)
		}
	}
	code, body = get(t, server.URL+"/?pod=web-1-b")
	if code != http.StatusOK || body != "b1\n" {
		t.Fatalf("a named pod is raw: %d %q", code, body)
	}
}

func TestLogsRefuseAPodOfAnotherContainer(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", noMutation)
	createContainer(t, c, ns, "other", noMutation)
	seedReadyPod(t, c, ns, "web-1-a", "web", true)
	seedReadyPod(t, c, ns, "other-1-a", "other", true)
	api := newFakeAPI(t, map[string]string{"web-1-a": "x\n", "other-1-a": "secret\n"})
	server := serveHandler(t, Logs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "web"})
	code, body := get(t, server.URL+"/?pod=other-1-a")
	if code != http.StatusNotFound || strings.Contains(body, "secret") {
		t.Fatalf("%d %q", code, body)
	}
	if paths, _, _ := api.seen(); len(paths) != 0 {
		t.Fatalf("the API server must not be asked: %v", paths)
	}
}

func TestLogsRefuseBadInput(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", noMutation)
	createContainer(t, c, ns, "empty", noMutation)
	seedReadyPod(t, c, ns, "web-1-a", "web", true)
	api := newFakeAPI(t, map[string]string{"web-1-a": "x\n"})
	server := serveHandler(t, Logs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "web"})
	if code, _ := get(t, server.URL+"/?tail=0"); code != http.StatusBadRequest {
		t.Fatalf("tail 0: %d", code)
	}
	if code, _ := get(t, server.URL+"/?tail=10001"); code != http.StatusBadRequest {
		t.Fatalf("tail 10001: %d", code)
	}
	resp, err := http.Post(server.URL+"/", "text/plain", nil)
	must(t, err)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("post: %d %v", resp.StatusCode, resp.Header)
	}
	missing := serveHandler(t, Logs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "ghost"})
	if code, _ := get(t, missing.URL+"/"); code != http.StatusNotFound {
		t.Fatalf("ghost: %d", code)
	}
	empty := serveHandler(t, Logs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "empty"})
	if code, _ := get(t, empty.URL+"/"); code != http.StatusConflict {
		t.Fatalf("no pod: %d", code)
	}
}

type execRig struct {
	proxy    *httptest.Server
	upstream *fakeAPI
	ns       string
}

func newExecRig(t *testing.T) execRig {
	t.Helper()
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", noMutation)
	createContainer(t, c, ns, "other", noMutation)
	seedReadyPod(t, c, ns, "web-1-a", "web", false)
	seedReadyPod(t, c, ns, "web-1-b", "web", true)
	seedReadyPod(t, c, ns, "other-1-a", "other", true)
	upstream := newFakeAPI(t, nil)
	proxy := serveHandler(t, Exec(upstream.deps()), c, client.ObjectKey{Namespace: ns, Name: "web"})
	return execRig{proxy: proxy, upstream: upstream, ns: ns}
}

func (rig execRig) dial(t *testing.T, query string, headers map[string]string) (*http.Response, *bufio.Reader, net.Conn) {
	t.Helper()
	host := strings.TrimPrefix(rig.proxy.URL, "http://")
	conn, err := net.Dial("tcp", host)
	must(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	if override, found := headers["Host"]; found {
		host = override
	}
	request := "GET /exec" + query + " HTTP/1.1\r\nHost: " + host + "\r\n"
	for name, value := range headers {
		if name != "Host" {
			request += name + ": " + value + "\r\n"
		}
	}
	_, err = conn.Write([]byte(request + "\r\n"))
	must(t, err)
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(reader, nil)
	must(t, err)
	return resp, reader, conn
}

func upgradeHeaders() map[string]string {
	return map[string]string{
		"Upgrade":                "websocket",
		"Connection":             "Upgrade",
		"Sec-WebSocket-Key":      "dGhlIHNhbXBsZSBub25jZQ==",
		"Sec-WebSocket-Version":  "13",
		"Sec-WebSocket-Protocol": "v5.channel.k8s.io",
	}
}

func TestExecProxiesToTheFirstReadyPod(t *testing.T) {
	rig := newExecRig(t)
	headers := upgradeHeaders()
	headers["Authorization"] = "Bearer user-token"
	headers["Cookie"] = "session=secret"
	headers["Impersonate-User"] = "system:admin"
	resp, reader, conn := rig.dial(t, "", headers)
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Protocol") != "v5.channel.k8s.io" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d headers %v body %q", resp.StatusCode, resp.Header, body)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(reader, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo %q %v", buf, err)
	}
	paths, queries, seen := rig.upstream.seen()
	if len(paths) != 1 || paths[0] != "/api/v1/namespaces/"+rig.ns+"/pods/web-1-b/exec" {
		t.Fatalf("paths %v", paths)
	}
	if queries[0].Get("container") != "app" || !reflect.DeepEqual(queries[0]["command"], []string{"/bin/sh"}) || queries[0].Get("tty") != "false" {
		t.Fatalf("query %v", queries[0])
	}
	if seen[0].Get("Authorization") != "Bearer proxy-token" || seen[0].Get("Cookie") != "" || seen[0].Get("Impersonate-User") != "" {
		t.Fatalf("upstream headers %v", seen[0])
	}
}

func TestExecUsesTheNamedPodCommandAndTTY(t *testing.T) {
	rig := newExecRig(t)
	resp, _, _ := rig.dial(t, "?pod=web-1-a&command=ls&command=-la&tty=true", upgradeHeaders())
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a pod that is not ready is a 409: %d", resp.StatusCode)
	}
	resp, _, _ = rig.dial(t, "?pod=web-1-b&command=ls&command=-la&tty=true", upgradeHeaders())
	paths, queries, _ := rig.upstream.seen()
	if resp.StatusCode != http.StatusSwitchingProtocols || len(paths) != 1 || queries[0].Get("tty") != "true" || !reflect.DeepEqual(queries[0]["command"], []string{"ls", "-la"}) {
		t.Fatalf("status %d paths %v queries %v", resp.StatusCode, paths, queries)
	}
}

func TestExecRefusesAPodOfAnotherContainer(t *testing.T) {
	rig := newExecRig(t)
	resp, _, _ := rig.dial(t, "?pod=other-1-a", upgradeHeaders())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if paths, _, _ := rig.upstream.seen(); len(paths) != 0 {
		t.Fatalf("the API server must not be asked: %v", paths)
	}
}

func TestExecRefusesForeignOriginAndMissingUpgrade(t *testing.T) {
	rig := newExecRig(t)
	foreign := upgradeHeaders()
	foreign["Host"], foreign["Origin"] = "console.example.test", "https://evil.test"
	if resp, _, _ := rig.dial(t, "", foreign); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	same := upgradeHeaders()
	same["Host"], same["Origin"] = "console.example.test", "https://console.example.test"
	if resp, _, _ := rig.dial(t, "", same); resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("same origin: %d", resp.StatusCode)
	}
	if resp, _, _ := rig.dial(t, "", map[string]string{}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no upgrade: %d", resp.StatusCode)
	}
}

func TestExecRefusesBadQueryAndMethod(t *testing.T) {
	rig := newExecRig(t)
	if resp, _, _ := rig.dial(t, "?tty=maybe", upgradeHeaders()); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad tty: %d", resp.StatusCode)
	}
	resp, err := http.Post(rig.proxy.URL+"/exec", "text/plain", nil)
	must(t, err)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d", resp.StatusCode)
	}
}

func TestExecWithoutAReadyPod(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", noMutation)
	seedReadyPod(t, c, ns, "web-1-a", "web", false)
	upstream := newFakeAPI(t, nil)
	rig := execRig{proxy: serveHandler(t, Exec(upstream.deps()), c, client.ObjectKey{Namespace: ns, Name: "web"}), upstream: upstream, ns: ns}
	if resp, _, _ := rig.dial(t, "", upgradeHeaders()); resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRestartStampsTheContainer(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", noMutation)
	server := serveHandler(t, Restart(Deps{Now: func() time.Time { return fixedNow }}), c, client.ObjectKey{Namespace: ns, Name: "web"})
	resp, err := http.Post(server.URL, "application/json", nil)
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(body), "2026-10-19T12:00:00Z") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	got := getContainer(t, c, ns, "web")
	if got.Annotations[v1alpha1.AnnotationRestartedAt] != "2026-10-19T12:00:00Z" {
		t.Fatalf("annotations %v", got.Annotations)
	}
	if code, _ := get(t, server.URL); code != http.StatusMethodNotAllowed {
		t.Fatalf("get: %d", code)
	}
	missing := serveHandler(t, Restart(Deps{}), c, client.ObjectKey{Namespace: ns, Name: "ghost"})
	resp, err = http.Post(missing.URL, "application/json", nil)
	must(t, err)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ghost: %d", resp.StatusCode)
	}
}

func TestRegisterSubresourcesAddsLogsExecAndRestart(t *testing.T) {
	reg := newRegistry()
	if err := registerWith(reg, Deps{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"logs", "exec", "restart"} {
		if _, found := reg.Lookup("containers", name); !found {
			t.Errorf("the subresource %s is not registered", name)
		}
	}
	if err := registerWith(reg, Deps{}); err == nil {
		t.Fatal("a second registration must fail")
	}
}
