//go:build e2e

package tenant

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const (
	execSubprotocol = "v5.channel.k8s.io"
	execTimeout     = 2 * time.Minute
	logLine         = "container-log-line"
	execProbe       = "exec-proof"
	stdoutChannel   = 1
)

func (e env) containerPath(container, subresource string) string {
	return "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/" + e.namespace + "/containers/" + container + "/" + subresource
}

func dialExec(e env, token, origin, path string, query url.Values) (*websocket.Conn, *http.Response, error) {
	header := http.Header{"Authorization": {"Bearer " + token}}
	if origin != "" {
		header.Set("Origin", origin)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: requestTimeout,
		Subprotocols:     []string{execSubprotocol},
		TLSClientConfig:  trustingTransport(e.roots).TLSClientConfig,
	}
	return dialer.Dial("wss://api."+e.host+path+"?"+query.Encode(), header)
}

func stdoutUntil(conn *websocket.Conn, needle string, timeout time.Duration) (string, bool) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	seen := ""
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return seen, false
		}
		if len(data) > 1 && data[0] == stdoutChannel {
			seen += string(data[1:])
		}
		if strings.Contains(seen, needle) {
			return seen, true
		}
	}
}

func TestContainerStreams(t *testing.T) {
	e := setupEnv(t)
	who := tokensFromEnv(t)
	container := os.Getenv("CONTAINER")
	if container == "" {
		t.Fatal("CONTAINER is not set")
	}

	t.Run("the owner reads the logs", func(t *testing.T) {
		status, body := e.bearer(t, who.owner, http.MethodGet, e.apiURL(e.containerPath(container, "logs")+"?tail=20"))
		requireStatus(t, "owner logs", status, http.StatusOK, body)
		if !strings.Contains(string(body), logLine) {
			t.Fatalf("the logs do not hold %q: %s", logLine, printable(body))
		}
	})

	t.Run("the logs of two pods are merged with the pod name", func(t *testing.T) {
		status, body := e.bearer(t, who.owner, http.MethodGet, e.apiURL(e.containerPath(container, "logs")))
		requireStatus(t, "merged logs", status, http.StatusOK, body)
		if !strings.Contains(string(body), "["+container+"-") {
			t.Fatalf("the merged logs hold no pod prefix: %s", printable(body))
		}
	})

	t.Run("the viewer reads the logs and cannot restart", func(t *testing.T) {
		status, body := e.bearer(t, who.viewer, http.MethodGet, e.apiURL(e.containerPath(container, "logs")+"?tail=1"))
		requireStatus(t, "viewer logs", status, http.StatusOK, body)
		status, body = e.bearer(t, who.viewer, http.MethodPost, e.apiURL(e.containerPath(container, "restart")))
		requireStatus(t, "viewer restart", status, http.StatusForbidden, body)
	})

	t.Run("a stranger is refused", func(t *testing.T) {
		for _, subresource := range []string{"logs", "restart"} {
			method := http.MethodGet
			if subresource == "restart" {
				method = http.MethodPost
			}
			status, body := e.bearer(t, who.stranger, method, e.apiURL(e.containerPath(container, subresource)))
			requireStatus(t, "stranger "+subresource, status, http.StatusForbidden, body)
		}
	})

	t.Run("a pod of another container is not found", func(t *testing.T) {
		status, body := e.bearer(t, who.owner, http.MethodGet, e.apiURL(e.containerPath(container, "logs")+"?pod=no-such-pod"))
		requireStatus(t, "foreign pod", status, http.StatusNotFound, body)
	})

	t.Run("a foreign origin and a viewer are refused on exec", func(t *testing.T) {
		query := url.Values{"command": {"echo", execProbe}}
		conn, resp, err := dialExec(e, who.owner, "https://evil.test", e.containerPath(container, "exec"), query)
		if err == nil {
			conn.Close()
			t.Fatal("exec accepted the origin https://evil.test")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("foreign origin: %v, want status 403", err)
		}
		conn, resp, err = dialExec(e, who.viewer, "", e.containerPath(container, "exec"), query)
		if err == nil {
			conn.Close()
			t.Fatal("exec accepted a viewer")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer: %v, want status 403", err)
		}
	})

	t.Run("the owner runs a command", func(t *testing.T) {
		conn, resp, err := dialExec(e, who.owner, "", e.containerPath(container, "exec"), url.Values{"command": {"echo", execProbe}})
		if err != nil {
			t.Fatalf("dial exec: %v (status %d)", err, statusOf(resp))
		}
		defer conn.Close()
		if seen, found := stdoutUntil(conn, execProbe, execTimeout); !found {
			t.Fatalf("the command output was not read: %q", seen)
		}
	})

	t.Run("restart stamps the container", func(t *testing.T) {
		status, body := e.bearer(t, who.owner, http.MethodPost, e.apiURL(e.containerPath(container, "restart")))
		requireStatus(t, "owner restart", status, http.StatusAccepted, body)
		if !strings.Contains(string(body), "restartedAt") {
			t.Fatalf("restart body: %s", printable(body))
		}
	})
}
