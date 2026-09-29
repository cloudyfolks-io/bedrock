package cliauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/authn/server"
	"github.com/cloudyfolks-io/bedrock/internal/operator"
)

type testOP struct {
	issuer string
	client *http.Client
}

func startTestOP(t *testing.T) testOP {
	t.Helper()
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../manifests/00-crds"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme, err := operator.Scheme()
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-system"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: "0.0.0-test", API: v1alpha1.APISpec{VIP: "203.0.113.10"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "platform.host"}, Spec: v1alpha1.SettingSpec{Value: "test"}}); err != nil {
		t.Fatal(err)
	}
	key, err := keys.Generate(rand.Reader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Save(ctx, c, key); err != nil {
		t.Fatal(err)
	}
	uiDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(uiDir, ".keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := server.New(ctx, server.Config{
		RestConfig:    cfg,
		Random:        rand.Reader,
		Clock:         time.Now,
		UI:            os.DirFS(uiDir),
		WebhookSecret: func(context.Context) (string, error) { return "test-bearer", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(handler)
	t.Cleanup(ts.Close)
	transport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tls.Dial(network, ts.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
		},
	}
	createOAuthClient(t, c, "bedrock-cli")
	createLocalUser(t, c, "alice", "correct horse battery staple")
	return testOP{issuer: "https://sso.test", client: &http.Client{Transport: transport}}
}

func createOAuthClient(t *testing.T, c client.Client, clientID string) {
	t.Helper()
	object := &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Name: clientID, Namespace: "bedrock-system"},
		Spec: v1alpha1.OAuthClientSpec{
			ClientID:     clientID,
			Public:       true,
			RedirectURIs: []string{"http://127.0.0.1/callback"},
			GrantTypes:   []string{v1alpha1.GrantAuthorizationCode, v1alpha1.GrantRefreshToken, v1alpha1.GrantDeviceCode},
		},
	}
	if err := c.Create(context.Background(), object); err != nil {
		t.Fatal(err)
	}
}

func createLocalUser(t *testing.T, c client.Client, username, password string) {
	t.Helper()
	ctx := context.Background()
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.UserObjectName(username), Namespace: "bedrock-system"},
		Spec:       v1alpha1.UserSpec{Username: username, Methods: []string{v1alpha1.MethodPassword}},
	}
	if err := c.Create(ctx, user); err != nil {
		t.Fatal(err)
	}
	hash, err := secret.Hash(rand.Reader, secret.DefaultParams, password)
	if err != nil {
		t.Fatal(err)
	}
	credName := v1alpha1.CredentialName(user.Name, v1alpha1.MethodPassword)
	credential := &v1alpha1.Credential{
		ObjectMeta: metav1.ObjectMeta{Name: credName, Namespace: "bedrock-system"},
		Spec:       v1alpha1.CredentialSpec{UserRef: user.Name, Method: v1alpha1.MethodPassword, SecretRef: credName},
	}
	if err := c.Create(ctx, credential); err != nil {
		t.Fatal(err)
	}
	credential.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"}
	object := secret.Object(credential, credName, map[string][]byte{"hash": []byte(hash)})
	if err := c.Create(ctx, object); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	credential.Status.EnrolledAt = &now
	if err := c.Status().Update(ctx, credential); err != nil {
		t.Fatal(err)
	}
}

func newBrowser(op testOP) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: op.client.Transport, Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
}

func postJSON(c *http.Client, url, csrf string, body any) (methods.Challenge, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return methods.Challenge{}, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return methods.Challenge{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		return methods.Challenge{}, err
	}
	defer resp.Body.Close()
	var challenge methods.Challenge
	if err := json.NewDecoder(resp.Body).Decode(&challenge); err != nil {
		return methods.Challenge{}, err
	}
	if resp.StatusCode >= 300 {
		return challenge, fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	return challenge, nil
}

func answerUsernameAndPassword(c *http.Client, issuer, csrf, username, password string) (methods.Challenge, error) {
	challenge, err := postJSON(c, issuer+"/api/v1/login/answer", csrf, methods.Answer{Type: methods.ChallengeUsername, Username: username})
	if err != nil {
		return methods.Challenge{}, err
	}
	return postJSON(c, issuer+"/api/v1/login/answer", challenge.CSRF, methods.Answer{Type: methods.ChallengePassword, Username: username, Password: password})
}

func answerDeviceLogin(op testOP, userCode, username, password string) error {
	browser := newBrowser(op)
	challenge, err := postJSON(browser, op.issuer+"/api/v1/login/device", "", map[string]string{"userCode": userCode})
	if err != nil {
		return err
	}
	challenge, err = answerUsernameAndPassword(browser, op.issuer, challenge.CSRF, username, password)
	if err != nil {
		return err
	}
	approve := true
	_, err = postJSON(browser, op.issuer+"/api/v1/login/answer", challenge.CSRF, methods.Answer{Type: methods.ChallengeDeviceConfirm, Approve: &approve})
	return err
}

func TestDeviceLogin(t *testing.T) {
	op := startTestOP(t)
	var browserErr error
	prompts := 0
	tokens, err := DeviceLogin(context.Background(), op.client, op.issuer, "bedrock-cli", func(uri, code string) {
		prompts++
		browserErr = answerDeviceLogin(op, code, "alice", "correct horse battery staple")
	})
	if err != nil {
		t.Fatal(err)
	}
	if browserErr != nil {
		t.Fatalf("browser: %v", browserErr)
	}
	if prompts != 1 {
		t.Fatalf("prompt called %d times", prompts)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.IDToken == "" {
		t.Fatalf("incomplete tokens: %+v", tokens)
	}
}

func TestRefresh(t *testing.T) {
	op := startTestOP(t)
	first, err := DeviceLogin(context.Background(), op.client, op.issuer, "bedrock-cli", func(uri, code string) {
		if err := answerDeviceLogin(op, code, "alice", "correct horse battery staple"); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := Refresh(context.Background(), op.client, op.issuer, "bedrock-cli", first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken == first.AccessToken {
		t.Fatal("refresh must issue a new access token")
	}
	if refreshed.RefreshToken == "" || refreshed.RefreshToken == first.RefreshToken {
		t.Fatal("refresh must rotate the refresh token")
	}
	if _, err := Refresh(context.Background(), op.client, op.issuer, "bedrock-cli", first.RefreshToken); err == nil {
		t.Fatal("a reused refresh token must fail")
	}
}
