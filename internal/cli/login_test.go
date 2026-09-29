package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/cliauth"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func fakeIDToken(sub string) string {
	payload, _ := json.Marshal(map[string]string{"sub": sub})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func successTokens(sub string) func(*http.Request) map[string]any {
	return func(*http.Request) map[string]any {
		return map[string]any{
			"access_token":  "access-" + sub,
			"refresh_token": "refresh-" + sub,
			"id_token":      fakeIDToken(sub),
			"token_type":    "Bearer",
			"expires_in":    3600,
		}
	}
}

func newFakeOP(t *testing.T, tokenResponse func(*http.Request) map[string]any) *httptest.Server {
	mux := http.NewServeMux()
	var issuer string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint":        issuer + "/authorize",
			"token_endpoint":                issuer + "/token",
			"device_authorization_endpoint": issuer + "/device_authorization",
		})
	})
	mux.HandleFunc("/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "devicecode",
			"user_code":                 "ABCD-EFGH",
			"verification_uri":          issuer + "/device",
			"verification_uri_complete": issuer + "/device?user_code=ABCD-EFGH",
			"expires_in":                600,
			"interval":                  0,
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		redirect := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")
		http.Redirect(w, r, redirect+"?code=authcode&state="+state, http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse(r))
	})
	mux.HandleFunc("/api/v1/cluster", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"server": "https://api.test:6443", "certificateAuthority": "-----BEGIN CERTIFICATE-----\nMA==\n-----END CERTIFICATE-----\n"})
	})
	ts := httptest.NewServer(mux)
	issuer = ts.URL
	t.Cleanup(ts.Close)
	return ts
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network must not be used for a fresh cache")
}

func TestLoginUsesFreshCache(t *testing.T) {
	home := t.TempDir()
	path := cliauth.CachePath(home, "https://sso.test")
	fresh := cliauth.Tokens{AccessToken: "still-good", RefreshToken: "r", IDToken: fakeIDToken("alice"), Expiry: time.Now().Add(time.Hour)}
	if err := cliauth.WriteCache(path, fresh); err != nil {
		t.Fatal(err)
	}
	deps := LoginDeps{
		Home: home,
		HTTP: func(string) (*http.Client, error) { return &http.Client{Transport: failingTransport{}}, nil },
		Open: func(string) error { t.Fatal("must not open a browser"); return nil },
		Now:  time.Now,
	}
	var stdout, stderr bytes.Buffer
	if code := RunLogin(context.Background(), []string{"--server", "https://sso.test"}, deps, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	if stdout.String() != "logged in as alice\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestLoginRefreshes(t *testing.T) {
	op := newFakeOP(t, successTokens("alice"))
	home := t.TempDir()
	path := cliauth.CachePath(home, op.URL)
	stale := cliauth.Tokens{AccessToken: "old", RefreshToken: "r", IDToken: fakeIDToken("alice"), Expiry: time.Now().Add(-time.Hour)}
	if err := cliauth.WriteCache(path, stale); err != nil {
		t.Fatal(err)
	}
	deps := LoginDeps{Home: home, HTTP: func(string) (*http.Client, error) { return op.Client(), nil }, Open: func(string) error { t.Fatal("must not open a browser"); return nil }, Now: time.Now}
	var stdout, stderr bytes.Buffer
	if code := RunLogin(context.Background(), []string{"--server", op.URL}, deps, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	cached, err := cliauth.ReadCache(path)
	if err != nil || cached.AccessToken != "access-alice" {
		t.Fatalf("cache %+v err %v", cached, err)
	}
}

func TestLoginDeviceFlowPrintsCode(t *testing.T) {
	op := newFakeOP(t, successTokens("alice"))
	home := t.TempDir()
	deps := LoginDeps{Home: home, HTTP: func(string) (*http.Client, error) { return op.Client(), nil }, Open: func(string) error { t.Fatal("device flow must not open a browser"); return nil }, Now: time.Now}
	var stdout, stderr bytes.Buffer
	if code := RunLogin(context.Background(), []string{"--server", op.URL}, deps, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "open "+op.URL+"/device?user_code=ABCD-EFGH") || !strings.Contains(stderr.String(), "code ABCD-EFGH") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if stdout.String() != "logged in as alice\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestLoginBrowserFlow(t *testing.T) {
	op := newFakeOP(t, successTokens("alice"))
	home := t.TempDir()
	client := op.Client()
	deps := LoginDeps{
		Home: home,
		HTTP: func(string) (*http.Client, error) { return client, nil },
		Open: func(url string) error {
			resp, err := client.Get(url)
			if err != nil {
				return err
			}
			return resp.Body.Close()
		},
		Now: time.Now,
	}
	var stdout, stderr bytes.Buffer
	if code := RunLogin(context.Background(), []string{"--server", op.URL, "--browser"}, deps, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	if stdout.String() != "logged in as alice\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestLoginNeedsServer(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := LoginDeps{Home: t.TempDir(), HTTP: func(string) (*http.Client, error) { return http.DefaultClient, nil }, Open: func(string) error { return nil }, Now: time.Now}
	if code := RunLogin(context.Background(), nil, deps, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "--server is required") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestRootListsLogin(t *testing.T) {
	if _, ok := Commands()["login"]; !ok {
		t.Fatal("bedrock login must be a command")
	}
}

func TestKubeconfigMergeKeepsOtherContexts(t *testing.T) {
	op := newFakeOP(t, successTokens("alice"))
	home := t.TempDir()
	kubeconfigPath := filepath.Join(home, ".kube", "config")
	if err := os.MkdirAll(filepath.Dir(kubeconfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := clientcmdapi.NewConfig()
	existing.Clusters["other"] = &clientcmdapi.Cluster{Server: "https://other.example.com"}
	existing.Contexts["other"] = &clientcmdapi.Context{Cluster: "other"}
	existing.CurrentContext = "other"
	if err := clientcmd.WriteToFile(*existing, kubeconfigPath); err != nil {
		t.Fatal(err)
	}
	deps := LoginDeps{Home: home, HTTP: func(string) (*http.Client, error) { return op.Client(), nil }, Open: func(string) error { t.Fatal("must not open a browser"); return nil }, Now: time.Now}
	var stdout, stderr bytes.Buffer
	if code := RunLogin(context.Background(), []string{"--server", op.URL, "--write-kubeconfig"}, deps, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	merged, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := merged.Contexts["other"]; !ok {
		t.Fatal("the merge must keep the other context")
	}
	if _, ok := merged.Contexts["bedrock"]; !ok {
		t.Fatal("the merge must add the bedrock context")
	}
	if merged.CurrentContext != "bedrock" {
		t.Fatalf("current context %q", merged.CurrentContext)
	}
}

func TestExecCredentialRefreshes(t *testing.T) {
	op := newFakeOP(t, successTokens("alice"))
	home := t.TempDir()
	path := cliauth.CachePath(home, op.URL)
	stale := cliauth.Tokens{AccessToken: "old", RefreshToken: "r", IDToken: fakeIDToken("alice"), Expiry: time.Now().Add(-time.Hour)}
	if err := cliauth.WriteCache(path, stale); err != nil {
		t.Fatal(err)
	}
	deps := LoginDeps{Home: home, HTTP: func(string) (*http.Client, error) { return op.Client(), nil }, Open: func(string) error { t.Fatal("must not open a browser"); return nil }, Now: time.Now}
	var stdout, stderr bytes.Buffer
	if code := RunLogin(context.Background(), []string{"--server", op.URL, "--exec-credential"}, deps, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	var credential struct {
		Kind   string `json:"kind"`
		Status struct {
			Token string `json:"token"`
		} `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &credential); err != nil {
		t.Fatalf("stdout %q: %v", stdout.String(), err)
	}
	if credential.Kind != "ExecCredential" || credential.Status.Token != "access-alice" {
		t.Fatalf("credential %+v", credential)
	}
}
