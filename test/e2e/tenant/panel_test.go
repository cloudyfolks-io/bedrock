//go:build e2e

package tenant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	streamSubprotocol = "bedrock.stream.v1"
	settingName       = "loki.retention-days"
	eventTimeout      = 15 * time.Second
	settingsPath      = "/apis/bedrock.cloudyfolks.io/v1alpha1/settings"
)

type sessionAnswer struct {
	User struct {
		Name   string   `json:"name"`
		Groups []string `json:"groups"`
	} `json:"user"`
	Admin bool   `json:"admin"`
	CSRF  string `json:"csrf"`
	Host  string `json:"host"`
}

type authzAnswer struct {
	Results []struct {
		Allowed bool `json:"allowed"`
	} `json:"results"`
}

type streamFrame struct {
	Type            string `json:"type"`
	ID              string `json:"id"`
	Event           string `json:"event"`
	Code            string `json:"code"`
	ResourceVersion string `json:"resourceVersion"`
	Object          struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	} `json:"object"`
}

type subscribeFrame struct {
	Op              string `json:"op"`
	ID              string `json:"id"`
	Group           string `json:"group"`
	Version         string `json:"version"`
	Resource        string `json:"resource"`
	ResourceVersion string `json:"resourceVersion"`
}

func (c *loginClient) request(t *testing.T, e env, method, path, csrf, contentType, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.consoleURL(path), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request %s: %v", path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	req.Header.Set("Origin", e.consoleURL(""))
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return resp.StatusCode, data
}

func decode[T any](t *testing.T, what string, data []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s: %v: %s", what, err, printable(data))
	}
	return out
}

func requireSession(t *testing.T, e env, c *loginClient) sessionAnswer {
	t.Helper()
	status, body := c.request(t, e, http.MethodGet, "/proxy/v1/session", "", "", "")
	requireStatus(t, "session", status, http.StatusOK, body)
	session := decode[sessionAnswer](t, "session", body)
	if session.User.Name != "admin" || !session.Admin || len(session.CSRF) != 43 || session.Host != e.host {
		t.Fatalf("session of %q: admin %v, csrf length %d, host %q; want admin, true, 43 and %s", session.User.Name, session.Admin, len(session.CSRF), session.Host, e.host)
	}
	return session
}

func requireAuthz(t *testing.T, e env, c *loginClient, csrf string) {
	t.Helper()
	body := `{"checks":[{"verb":"delete","group":"bedrock.cloudyfolks.io","resource":"hosts"},{"verb":"get","resource":"nodes"}]}`
	status, data := c.request(t, e, http.MethodPost, "/proxy/v1/authz", csrf, "application/json", body)
	requireStatus(t, "authz", status, http.StatusOK, data)
	answer := decode[authzAnswer](t, "authz", data)
	if len(answer.Results) != 2 || !answer.Results[0].Allowed || !answer.Results[1].Allowed {
		t.Fatalf("authz answer %s, want both allowed for admin", printable(data))
	}
}

func requireCSRFGuard(t *testing.T, e env, c *loginClient, csrf, value string) {
	t.Helper()
	patch := `{"spec":{"value":"` + value + `"}}`
	status, body := c.request(t, e, http.MethodPatch, settingsPath+"/"+settingName, "", "application/merge-patch+json", patch)
	if status != http.StatusForbidden || !strings.Contains(string(body), `"csrf"`) {
		t.Fatalf("patch without csrf: status %d: %s, want 403 csrf", status, printable(body))
	}
	status, body = c.request(t, e, http.MethodPatch, settingsPath+"/"+settingName, csrf, "application/merge-patch+json", patch)
	requireStatus(t, "patch with csrf", status, http.StatusOK, body)
}

func (e env) settingValue(t *testing.T) string {
	t.Helper()
	obj, err := e.fetch(bedrockSetting, "", settingName)
	if err != nil {
		t.Fatalf("read setting %s: %v", settingName, err)
	}
	value, _, _ := unstructured.NestedString(obj.Object, "spec", "value")
	return value
}

func (e env) patchSetting(t *testing.T, value string) {
	t.Helper()
	obj := objectOf(bedrockSetting)
	obj.SetName(settingName)
	patch := []byte(`{"spec":{"value":"` + value + `"}}`)
	if err := e.k8s.Patch(context.Background(), obj, client.RawPatch(types.MergePatchType, patch)); err != nil {
		t.Fatalf("patch setting %s: %v", settingName, err)
	}
}

func (c *loginClient) dialStream(e env, origin string) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: requestTimeout,
		Subprotocols:     []string{streamSubprotocol},
		TLSClientConfig:  trustingTransport(e.roots).TLSClientConfig,
		Jar:              c.jar,
	}
	return dialer.Dial("wss://console."+e.host+"/proxy/v1/stream", http.Header{"Origin": {origin}})
}

func readFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) streamFrame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read stream frame: %v", err)
	}
	return decode[streamFrame](t, "stream frame", data)
}

func listResourceVersion(t *testing.T, e env, c *loginClient) string {
	t.Helper()
	status, body := c.request(t, e, http.MethodGet, settingsPath, "", "", "")
	requireStatus(t, "list settings", status, http.StatusOK, body)
	list := decode[struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}](t, "settings list", body)
	if list.Metadata.ResourceVersion == "" {
		t.Fatalf("settings list carries no resourceVersion: %s", printable(body))
	}
	return list.Metadata.ResourceVersion
}

func requireStreamEvent(t *testing.T, e env, c *loginClient, previous string) {
	t.Helper()
	conn, resp, err := c.dialStream(e, e.consoleURL(""))
	if err != nil {
		t.Fatalf("stream handshake: %v (status %d)", err, statusOf(resp))
	}
	defer conn.Close()
	if hello := readFrame(t, conn, eventTimeout); hello.Type != "hello" {
		t.Fatalf("first stream frame is %q, want hello", hello.Type)
	}
	subscribe := subscribeFrame{Op: "subscribe", ID: "s1", Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Resource: "settings", ResourceVersion: listResourceVersion(t, e, c)}
	if err := conn.WriteJSON(subscribe); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if frame := readFrame(t, conn, eventTimeout); frame.Type != "subscribed" || frame.ID != "s1" {
		t.Fatalf("answer to subscribe = %+v, want subscribed s1", frame)
	}
	next := "5"
	if previous == next {
		next = "6"
	}
	e.patchSetting(t, next)
	frame := readFrame(t, conn, eventTimeout)
	if frame.Type != "event" || frame.Event != "MODIFIED" || frame.Object.Metadata.Name != settingName {
		t.Fatalf("frame after the patch = %+v, want a MODIFIED event of %s", frame, settingName)
	}
	e.patchSetting(t, previous)
	if err := conn.WriteJSON(subscribeFrame{Op: "subscribe", ID: "s2", Group: "", Version: "v1", Resource: "secrets", ResourceVersion: "1"}); err != nil {
		t.Fatalf("subscribe to secrets: %v", err)
	}
	for {
		refused := readFrame(t, conn, eventTimeout)
		if refused.ID == "s2" {
			if refused.Type != "error" || refused.Code != "resource_not_allowed" {
				t.Fatalf("answer to the secrets subscription = %+v, want error resource_not_allowed", refused)
			}
			return
		}
	}
}

func TestPanelLoginAndStream(t *testing.T) {
	e := setupEnv(t)
	adminToken := requireEnv(t, "TOKEN_ADMIN")
	c := newLoginClient(t, e)
	c.signInAsAdmin(t, e, requireEnv(t, "ADMIN_PASSWORD"))

	var session sessionAnswer
	t.Run("the session names the admin", func(t *testing.T) {
		session = requireSession(t, e, c)
	})

	t.Run("access checks answer for the admin", func(t *testing.T) {
		requireAuthz(t, e, c, session.CSRF)
	})

	previous := e.settingValue(t)

	t.Run("writes need the csrf header", func(t *testing.T) {
		requireCSRFGuard(t, e, c, session.CSRF, previous)
	})

	t.Run("the stream delivers a setting change", func(t *testing.T) {
		requireStreamEvent(t, e, c, previous)
	})

	t.Run("the stream refuses a foreign origin", func(t *testing.T) {
		conn, resp, err := c.dialStream(e, "https://evil.test")
		if err == nil {
			conn.Close()
			t.Fatalf("stream accepted the origin https://evil.test")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("foreign origin: %v, want status 403", err)
		}
	})

	t.Run("the API host accepts the bearer and refuses junk", func(t *testing.T) {
		status, body := e.bearer(t, adminToken, http.MethodGet, e.apiURL("/apis/bedrock.cloudyfolks.io/v1alpha1/hosts"))
		requireStatus(t, "api with the admin token", status, http.StatusOK, body)
		status, body = e.bearer(t, "junk", http.MethodGet, e.apiURL("/apis/bedrock.cloudyfolks.io/v1alpha1/hosts"))
		requireStatus(t, "api with junk", status, http.StatusUnauthorized, body)
		status, body = e.bearer(t, adminToken, http.MethodGet, e.apiURL("/oauth/login"))
		requireStatus(t, "oauth login on the API host", status, http.StatusNotFound, body)
	})
}
