package compute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/proxy"
)

func TestRegisterSubresources(t *testing.T) {
	reg := proxy.NewSubresources()
	deps := actionDeps(newFakeKubeVirt(t))
	must(t, registerWith(reg, deps))
	for _, name := range []string{"start", "stop", "restart", "migrate", "snapshot", "console/vnc", "console/serial"} {
		if _, found := reg.Lookup("virtualmachines", name); !found {
			t.Errorf("%s is not registered", name)
		}
	}
	if err := registerWith(reg, deps); err == nil {
		t.Fatal("a second registration must fail")
	}
	if _, found := reg.Lookup("virtualmachines", "delete"); found {
		t.Fatal("only the seven subresources are registered")
	}
}

func callSubresource(t *testing.T, reg *proxy.Subresources, c client.Client, key client.ObjectKey, name, method, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	handler, found := reg.Lookup("virtualmachines", name)
	if !found {
		t.Fatalf("%s is not registered", name)
	}
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	for header, value := range headers {
		req.Header.Set(header, value)
	}
	rec := httptest.NewRecorder()
	handler(context.Background(), c, key, rec, req)
	return rec
}

func TestSubresourceAdapterRoutes(t *testing.T) {
	c, _ := startEnv(t)
	key := actionVM(t, c, "Running")
	reg := proxy.NewSubresources()
	must(t, registerWith(reg, actionDeps(newFakeKubeVirt(t))))
	cases := []struct {
		name, method, body string
		want               int
	}{
		{"start", http.MethodPost, "", http.StatusAccepted},
		{"start", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"snapshot", http.MethodPost, "", http.StatusCreated},
		{"snapshot", http.MethodGet, "", http.StatusOK},
		{"snapshot", http.MethodPost, `{"retention":"721h"}`, http.StatusBadRequest},
		{"snapshot", http.MethodPost, `{`, http.StatusBadRequest},
		{"snapshot", http.MethodDelete, "", http.StatusMethodNotAllowed},
		{"console/vnc", http.MethodGet, "", http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := callSubresource(t, reg, c, key, tc.name, tc.method, tc.body, nil)
		if rec.Code != tc.want {
			t.Errorf("%s %s gave %d: %s", tc.method, tc.name, rec.Code, rec.Body)
		}
	}
}

func TestSubresourceAdapterRendersStatusErrors(t *testing.T) {
	c, _ := startEnv(t)
	key := actionVM(t, c, "Stopped")
	reg := proxy.NewSubresources()
	must(t, registerWith(reg, actionDeps(newFakeKubeVirt(t))))
	rec := callSubresource(t, reg, c, key, "restart", http.MethodPost, "", nil)
	var status metav1.Status
	must(t, json.Unmarshal(rec.Body.Bytes(), &status))
	if rec.Code != http.StatusConflict || status.Code != http.StatusConflict || status.Kind != "Status" || status.Reason != metav1.StatusReasonConflict || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %+v", rec.Code, status)
	}
}

func TestSubresourceSnapshotUsesRetention(t *testing.T) {
	c, _ := startEnv(t)
	key := actionVM(t, c, "Running")
	reg := proxy.NewSubresources()
	must(t, registerWith(reg, actionDeps(newFakeKubeVirt(t))))
	rec := callSubresource(t, reg, c, key, "snapshot", http.MethodPost, `{"retention":"24h"}`, nil)
	var body map[string]string
	must(t, json.Unmarshal(rec.Body.Bytes(), &body))
	if rec.Code != http.StatusCreated || body["expiresAt"] != "2026-10-09T12:00:00Z" {
		t.Fatalf("%d %+v", rec.Code, body)
	}
}
