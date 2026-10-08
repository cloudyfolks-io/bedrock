//go:build e2e

package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	serialSubprotocol = "plain.kubevirt.io"
	stopTimeout       = 3 * time.Minute
	startTimeout      = 10 * time.Minute
	snapshotTimeout   = 3 * time.Minute
	bannerTimeout     = 3 * time.Minute
)

type tokens struct {
	owner    string
	viewer   string
	stranger string
	admin    string
}

func tokensFromEnv(t *testing.T) tokens {
	t.Helper()
	return tokens{
		owner:    requireEnv(t, "TOKEN_OWNER"),
		viewer:   requireEnv(t, "TOKEN_VIEWER"),
		stranger: requireEnv(t, "TOKEN_STRANGER"),
		admin:    requireEnv(t, "TOKEN_ADMIN"),
	}
}

func (e env) post(t *testing.T, token, subresource string) (int, []byte) {
	t.Helper()
	return e.bearer(t, token, http.MethodPost, e.apiURL(e.vmPath(subresource)))
}

func requireActionsRefused(t *testing.T, e env, who, token string) {
	t.Helper()
	for _, action := range []string{"start", "stop", "restart", "snapshot"} {
		status, body := e.post(t, token, action)
		requireStatus(t, who+" "+action, status, http.StatusForbidden, body)
	}
}

func migrationName(t *testing.T, body []byte) string {
	t.Helper()
	var answer struct {
		Migration string `json:"migration"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.Migration == "" {
		t.Fatalf("migrate answer %q holds no migration name: %v", body, err)
	}
	return answer.Migration
}

func (e env) snapshotsOfVM() []string {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(kubevirtSnapshot.GroupVersion().WithKind("VirtualMachineSnapshotList"))
	if err := e.k8s.List(context.Background(), list, client.InNamespace(e.namespace)); err != nil {
		return nil
	}
	names := []string{}
	for _, item := range list.Items {
		source, _, _ := unstructured.NestedString(item.Object, "spec", "source", "name")
		ready, _, _ := unstructured.NestedBool(item.Object, "status", "readyToUse")
		if source == e.vm && ready {
			names = append(names, item.GetName())
		}
	}
	return names
}

func TestProxyActions(t *testing.T) {
	e := setupEnv(t)
	who := tokensFromEnv(t)

	t.Run("users without a role change nothing", func(t *testing.T) {
		requireActionsRefused(t, e, "viewer", who.viewer)
		requireActionsRefused(t, e, "stranger", who.stranger)
		if phase := e.vmField("status", "phase"); phase != "Running" {
			t.Fatalf("vm phase after refused actions = %q, want Running", phase)
		}
	})

	t.Run("stop answers 202 and the VM stops", func(t *testing.T) {
		status, body := e.post(t, who.owner, "stop")
		requireStatus(t, "stop", status, http.StatusAccepted, body)
		e.waitVMPhase(t, "Stopped", stopTimeout)
	})

	t.Run("a stopped VM refuses restart and console", func(t *testing.T) {
		status, body := e.post(t, who.owner, "restart")
		requireStatus(t, "restart of a stopped VM", status, http.StatusConflict, body)
		_, resp, err := dialSerial(e, who.owner, "", e.vmPath("console/serial"))
		if err == nil {
			t.Fatalf("console of a stopped VM connected")
		}
		if resp == nil || resp.StatusCode != http.StatusConflict {
			t.Fatalf("console of a stopped VM: %v, want status 409", err)
		}
	})

	t.Run("start answers 202 and the VM runs again", func(t *testing.T) {
		status, body := e.post(t, who.owner, "start")
		requireStatus(t, "start", status, http.StatusAccepted, body)
		e.waitVMPhase(t, "Running", startTimeout)
	})

	t.Run("restart answers 202 and replaces the instance", func(t *testing.T) {
		before := e.instanceUID()
		status, body := e.post(t, who.owner, "restart")
		requireStatus(t, "restart", status, http.StatusAccepted, body)
		waitFor(t, startTimeout, "a new VirtualMachineInstance", func() (bool, string) {
			uid := e.instanceUID()
			return uid != "" && uid != before, uid
		})
		e.waitVMPhase(t, "Running", startTimeout)
	})

	t.Run("migrate is for cluster admins", func(t *testing.T) {
		status, body := e.post(t, who.owner, "migrate")
		requireStatus(t, "owner migrate", status, http.StatusForbidden, body)
		status, body = e.post(t, who.admin, "migrate")
		requireStatus(t, "admin migrate", status, http.StatusAccepted, body)
		name := migrationName(t, body)
		migration, err := e.fetch(kubevirtMigration, e.namespace, name)
		if err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
		if err := e.k8s.Delete(context.Background(), migration); err != nil {
			t.Fatalf("delete migration %s: %v", name, err)
		}
	})

	t.Run("snapshot answers 201 and is listed", func(t *testing.T) {
		e.waitVMPhase(t, "Running", startTimeout)
		status, body := e.post(t, who.owner, "snapshot")
		requireStatus(t, "snapshot", status, http.StatusCreated, body)
		waitFor(t, snapshotTimeout, "a ready VirtualMachineSnapshot", func() (bool, string) {
			names := e.snapshotsOfVM()
			return len(names) > 0, strings.Join(names, ",")
		})
		status, body = e.bearer(t, who.owner, http.MethodGet, e.apiURL(e.vmPath("snapshot")))
		requireStatus(t, "list snapshots", status, http.StatusOK, body)
		if !strings.Contains(string(body), e.snapshotsOfVM()[0]) {
			t.Fatalf("snapshot list %s lacks %s", body, e.snapshotsOfVM()[0])
		}
	})
}

func serialPathFallback(e env) string {
	if path := e.vmField("status", "console", "serialPath"); path != "" {
		return path
	}
	return e.vmPath("console/serial")
}

func dialSerial(e env, token, origin, path string) (*websocket.Conn, *http.Response, error) {
	header := http.Header{"Authorization": {"Bearer " + token}}
	if origin != "" {
		header.Set("Origin", origin)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: requestTimeout,
		Subprotocols:     []string{serialSubprotocol},
		TLSClientConfig:  trustingTransport(e.roots).TLSClientConfig,
	}
	return dialer.Dial("wss://api."+e.host+path, header)
}

func readUntil(conn *websocket.Conn, needle string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	seen := ""
	for time.Now().Before(deadline) {
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte("\n")); err != nil {
			return seen, false
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := conn.ReadMessage()
		if err == nil {
			seen += string(data)
		}
		if strings.Contains(seen, needle) {
			return seen, true
		}
	}
	return seen, false
}

func TestConsoleSerial(t *testing.T) {
	e := setupEnv(t)
	who := tokensFromEnv(t)
	path := serialPathFallback(e)

	t.Run("a foreign origin is refused", func(t *testing.T) {
		conn, resp, err := dialSerial(e, who.owner, "https://evil.test", path)
		if err == nil {
			conn.Close()
			t.Fatalf("console accepted the origin https://evil.test")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("foreign origin: %v, want status 403", err)
		}
	})

	t.Run("a user without a role is refused", func(t *testing.T) {
		conn, resp, err := dialSerial(e, who.stranger, "", path)
		if err == nil {
			conn.Close()
			t.Fatalf("console accepted a user without a role")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("stranger: %v, want status 403", err)
		}
	})

	t.Run("the owner reaches the guest login prompt", func(t *testing.T) {
		conn, resp, err := dialSerial(e, who.owner, "", path)
		if err != nil {
			t.Fatalf("console handshake: %v (response %v)", err, resp)
		}
		defer conn.Close()
		if conn.Subprotocol() != serialSubprotocol {
			t.Fatalf("subprotocol = %q, want %q", conn.Subprotocol(), serialSubprotocol)
		}
		seen, ok := readUntil(conn, "login:", bannerTimeout)
		if !ok {
			t.Fatalf("no login prompt within %s; read %q", bannerTimeout, seen)
		}
	})
}
