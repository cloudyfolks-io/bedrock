package compute

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fakeConsoleUpstream struct {
	mu      sync.Mutex
	headers []http.Header
	paths   []string
	Server  *httptest.Server
}

func newFakeConsoleUpstream(t *testing.T) *fakeConsoleUpstream {
	t.Helper()
	fake := &fakeConsoleUpstream{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.headers = append(fake.headers, r.Header.Clone())
		fake.paths = append(fake.paths, r.URL.Path)
		fake.mu.Unlock()
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Protocol: " + r.Header.Get("Sec-WebSocket-Protocol") + "\r\n\r\n")
		_ = buffered.Flush()
		_, _ = io.Copy(conn, buffered)
	}))
	t.Cleanup(fake.Server.Close)
	return fake
}

func (f *fakeConsoleUpstream) seen() ([]http.Header, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]http.Header(nil), f.headers...), append([]string(nil), f.paths...)
}

type consoleRig struct {
	proxy    *httptest.Server
	upstream *fakeConsoleUpstream
	key      client.ObjectKey
}

func newConsoleRig(t *testing.T, phase string, serial bool) consoleRig {
	t.Helper()
	c, _ := startEnv(t)
	key := actionVM(t, c, phase)
	upstream := newFakeConsoleUpstream(t)
	deps := Deps{Config: &rest.Config{Host: upstream.Server.URL, BearerToken: "proxy-token"}, Now: time.Now}
	handler := VNC(deps)
	if serial {
		handler = Serial(deps)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(context.Background(), c, key, w, r)
	}))
	t.Cleanup(proxy.Close)
	return consoleRig{proxy: proxy, upstream: upstream, key: key}
}

func (rig consoleRig) dial(t *testing.T, headers map[string]string) (*http.Response, *bufio.Reader, net.Conn) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(rig.proxy.URL, "http://"))
	must(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	host := strings.TrimPrefix(rig.proxy.URL, "http://")
	if override, found := headers["Host"]; found {
		host = override
	}
	request := "GET /console HTTP/1.1\r\nHost: " + host + "\r\n"
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
		"Sec-WebSocket-Protocol": "plain.kubevirt.io",
	}
}

func requireEcho(t *testing.T, reader *bufio.Reader, conn net.Conn) {
	t.Helper()
	_, err := conn.Write([]byte("hello"))
	must(t, err)
	buf := make([]byte, 5)
	_, err = io.ReadFull(reader, buf)
	must(t, err)
	if string(buf) != "hello" {
		t.Fatalf("echo %q", buf)
	}
}

func TestConsoleProxiesVNC(t *testing.T) {
	rig := newConsoleRig(t, "Running", false)
	resp, reader, conn := rig.dial(t, upgradeHeaders())
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	requireEcho(t, reader, conn)
	_, paths := rig.upstream.seen()
	want := "/apis/subresources.kubevirt.io/v1/namespaces/" + rig.key.Namespace + "/virtualmachineinstances/web-1/vnc"
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("paths %v", paths)
	}
}

func TestConsoleSerialUsesConsoleSubresource(t *testing.T) {
	rig := newConsoleRig(t, "Running", true)
	resp, _, _ := rig.dial(t, upgradeHeaders())
	_, paths := rig.upstream.seen()
	if resp.StatusCode != http.StatusSwitchingProtocols || len(paths) != 1 || !strings.HasSuffix(paths[0], "/virtualmachineinstances/web-1/console") {
		t.Fatalf("status %d paths %v", resp.StatusCode, paths)
	}
}

func TestConsoleStripsCallerCredentials(t *testing.T) {
	rig := newConsoleRig(t, "Running", false)
	headers := upgradeHeaders()
	headers["Authorization"] = "Bearer user-token"
	headers["Cookie"] = "session=secret"
	headers["Impersonate-User"] = "system:admin"
	headers["Impersonate-Group"] = "system:masters"
	headers["Impersonate-Extra-Scopes"] = "all"
	headers["X-Remote-User"] = "admin"
	headers["X-Remote-Group"] = "system:masters"
	headers["X-Forwarded-For"] = "203.0.113.9"
	headers["X-Custom"] = "value"
	resp, _, _ := rig.dial(t, headers)
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Protocol") != "plain.kubevirt.io" {
		t.Fatalf("status %d headers %v", resp.StatusCode, resp.Header)
	}
	seen, _ := rig.upstream.seen()
	if len(seen) != 1 || seen[0].Get("Authorization") != "Bearer proxy-token" || seen[0].Get("Cookie") != "" || seen[0].Get("Sec-WebSocket-Protocol") != "plain.kubevirt.io" {
		t.Fatalf("upstream headers %v", seen)
	}
	allowed := map[string]bool{
		"Authorization": true, "Upgrade": true, "Connection": true, "Sec-Websocket-Key": true, "Sec-Websocket-Version": true,
		"Sec-Websocket-Protocol": true, "User-Agent": true, "Accept-Encoding": true,
	}
	for name := range seen[0] {
		if !allowed[name] {
			t.Errorf("the header %s reached the upstream", name)
		}
	}
}

func TestConsoleRefusesForeignOrigin(t *testing.T) {
	rig := newConsoleRig(t, "Running", false)
	foreign := upgradeHeaders()
	foreign["Host"], foreign["Origin"] = "console.example.test", "https://evil.test"
	if resp, _, _ := rig.dial(t, foreign); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if seen, _ := rig.upstream.seen(); len(seen) != 0 {
		t.Fatal("the upstream must see nothing")
	}
	same := upgradeHeaders()
	same["Host"], same["Origin"] = "console.example.test", "https://console.example.test"
	if resp, _, _ := rig.dial(t, same); resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("same origin status %d", resp.StatusCode)
	}
	if resp, _, _ := rig.dial(t, upgradeHeaders()); resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("no origin status %d", resp.StatusCode)
	}
}

func TestConsoleMissingVMReachesNoUpstream(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	upstream := newFakeConsoleUpstream(t)
	deps := Deps{Config: &rest.Config{Host: upstream.Server.URL}, Now: time.Now}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		VNC(deps)(context.Background(), c, client.ObjectKey{Namespace: ns, Name: "ghost"}, w, r)
	}))
	defer proxy.Close()
	rig := consoleRig{proxy: proxy, upstream: upstream}
	if resp, _, _ := rig.dial(t, upgradeHeaders()); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if seen, _ := upstream.seen(); len(seen) != 0 {
		t.Fatal("the upstream must see nothing")
	}
}

func TestConsoleNeedsRunningVM(t *testing.T) {
	for phase, want := range map[string]int{
		"Stopped": http.StatusConflict, "Starting": http.StatusConflict, "Stopping": http.StatusConflict, "Failed": http.StatusConflict,
		"Running": http.StatusSwitchingProtocols, "Migrating": http.StatusSwitchingProtocols,
	} {
		rig := newConsoleRig(t, phase, false)
		if resp, _, _ := rig.dial(t, upgradeHeaders()); resp.StatusCode != want {
			t.Errorf("%s gave %d", phase, resp.StatusCode)
		}
	}
}

func TestConsoleRefusesPlainHTTP(t *testing.T) {
	rig := newConsoleRig(t, "Running", false)
	if resp, _, _ := rig.dial(t, map[string]string{}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if seen, _ := rig.upstream.seen(); len(seen) != 0 {
		t.Fatal("the upstream must see nothing")
	}
}

func TestConsoleUpstreamDown(t *testing.T) {
	rig := newConsoleRig(t, "Running", false)
	rig.upstream.Server.Close()
	resp, _, _ := rig.dial(t, upgradeHeaders())
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), `"kind":"Status"`) {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
}
