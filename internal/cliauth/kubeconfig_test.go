package cliauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

func TestKubeconfigGolden(t *testing.T) {
	data, err := Kubeconfig("https://api.example.com:6443", []byte("-----BEGIN CERTIFICATE-----\nMA==\n-----END CERTIFICATE-----\n"), "https://sso.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	config, err := clientcmd.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	cluster, ok := config.Clusters["bedrock"]
	if !ok || cluster.Server != "https://api.example.com:6443" || len(cluster.CertificateAuthorityData) == 0 {
		t.Fatalf("cluster %+v", cluster)
	}
	authInfo, ok := config.AuthInfos["bedrock"]
	if !ok || authInfo.Exec == nil || authInfo.Exec.Command != "bedrock" {
		t.Fatalf("authInfo %+v", authInfo)
	}
	want := []string{"login", "--server", "https://sso.example.com", "--exec-credential"}
	if len(authInfo.Exec.Args) != len(want) {
		t.Fatalf("args %v", authInfo.Exec.Args)
	}
	for i := range want {
		if authInfo.Exec.Args[i] != want[i] {
			t.Fatalf("args %v", authInfo.Exec.Args)
		}
	}
	if config.CurrentContext != "bedrock" {
		t.Fatalf("current context %q", config.CurrentContext)
	}
}

func TestKubeconfigCarriesTheCAFile(t *testing.T) {
	data, err := Kubeconfig("https://api.example.com:6443", nil, "https://sso.example.com", "/etc/bedrock/ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	config, err := clientcmd.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	want := "login --server https://sso.example.com --ca-file /etc/bedrock/ca.crt --exec-credential"
	if got := strings.Join(config.AuthInfos["bedrock"].Exec.Args, " "); got != want {
		t.Fatalf("args %q", got)
	}
}

func TestExecCredentialJSON(t *testing.T) {
	expiry := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	data, err := ExecCredential(Tokens{AccessToken: "tok", Expiry: expiry})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Kind       string `json:"kind"`
		APIVersion string `json:"apiVersion"`
		Status     struct {
			Token               string `json:"token"`
			ExpirationTimestamp string `json:"expirationTimestamp"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != "ExecCredential" || decoded.APIVersion != "client.authentication.k8s.io/v1" {
		t.Fatalf("decoded %+v", decoded)
	}
	if decoded.Status.Token != "tok" || decoded.Status.ExpirationTimestamp != "2026-09-28T12:00:00Z" {
		t.Fatalf("decoded %+v", decoded)
	}
}

func TestClusterInfo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cluster" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"server": "https://api.example.com:6443", "certificateAuthority": "-----BEGIN CERTIFICATE-----\nMA==\n-----END CERTIFICATE-----\n"})
	}))
	defer ts.Close()
	server, ca, err := ClusterInfo(context.Background(), ts.Client(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if server != "https://api.example.com:6443" || string(ca) != "-----BEGIN CERTIFICATE-----\nMA==\n-----END CERTIFICATE-----\n" {
		t.Fatalf("server %q ca %q", server, ca)
	}
}
