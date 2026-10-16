package k0s

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testJoinBearer = "abcdef.0123456789abcdef"

func serverCAPEM(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

func joinToken(t *testing.T, server string, caPEM []byte, bearer string) string {
	t.Helper()
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: k0s
  cluster:
    server: %s
    certificate-authority-data: %s
contexts:
- name: k0s
  context:
    cluster: k0s
    user: controller-join
current-context: k0s
users:
- name: controller-join
  user:
    token: %s
`, server, base64.StdEncoding.EncodeToString(caPEM), bearer)
	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	if _, err := writer.Write([]byte(kubeconfig)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(zipped.Bytes())
}

func TestFetchCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta1/ca" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+testJoinBearer {
			http.Error(w, "unexpected request", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string][]byte{"key": []byte("KEY"), "cert": []byte("CERT"), "saKey": {}, "saPub": {}})
	}))
	defer srv.Close()
	got, err := FetchCA(context.Background(), joinToken(t, srv.URL, serverCAPEM(srv), testJoinBearer))
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Cert) != "CERT" || string(got.Key) != "KEY" {
		t.Fatalf("ca %+v", got)
	}
}

func TestFetchCARefused(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := FetchCA(context.Background(), joinToken(t, srv.URL, serverCAPEM(srv), testJoinBearer))
	if err == nil || !strings.Contains(err.Error(), "k0s join api answered 401: invalid token") {
		t.Fatalf("err %v", err)
	}
}

func TestFetchCARefusedCutsLongBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(strings.Repeat("x", 500)))
	}))
	defer srv.Close()
	_, err := FetchCA(context.Background(), joinToken(t, srv.URL, serverCAPEM(srv), testJoinBearer))
	want := "k0s join api answered 403: " + strings.Repeat("x", 200)
	if err == nil || err.Error() != want {
		t.Fatalf("err %v", err)
	}
}

func TestFetchCABadToken(t *testing.T) {
	_, err := FetchCA(context.Background(), "not base64!")
	if err == nil || !strings.HasPrefix(err.Error(), "decode k0s token") {
		t.Fatalf("err %v", err)
	}
}
