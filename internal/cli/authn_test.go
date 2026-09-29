package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/operator"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestAuthnServeFlags(t *testing.T) {
	defaults, err := parseServeFlags(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if defaults != (serveOptions{listen: ":8443", tlsCert: "/tls/tls.crt", tlsKey: "/tls/tls.key"}) {
		t.Fatalf("defaults %+v", defaults)
	}
	custom, err := parseServeFlags([]string{"--listen", "127.0.0.1:9443", "--tls-cert", "/a.crt", "--tls-key", "/a.key", "--ui-dir", "/srv/ui"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if custom != (serveOptions{listen: "127.0.0.1:9443", tlsCert: "/a.crt", tlsKey: "/a.key", uiDir: "/srv/ui"}) {
		t.Fatalf("custom %+v", custom)
	}
	for _, args := range [][]string{{"--nope"}, {"extra"}} {
		if _, err := parseServeFlags(args, io.Discard); err == nil {
			t.Fatalf("args %v must fail", args)
		}
	}
	untouched := AuthnDeps{
		RestConfig: func() (*rest.Config, error) { return nil, errors.New("the API must not be reached") },
		Listen:     func(string, string) (net.Listener, error) { return nil, errors.New("nothing may listen") },
	}
	var stdout, stderr bytes.Buffer
	if code := RunAuthnServe(context.Background(), []string{"--nope"}, untouched, &stdout, &stderr); code != 2 {
		t.Fatalf("bad flag: exit %d", code)
	}
	stderr.Reset()
	missing := filepath.Join(t.TempDir(), "missing")
	if code := RunAuthnServe(context.Background(), []string{"--tls-cert", missing + ".crt", "--tls-key", missing + ".key"}, untouched, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "tls:") {
		t.Fatalf("missing TLS pair: exit %d, stderr %q", code, stderr.String())
	}
}

func TestAuthnServeServesHealthz(t *testing.T) {
	scheme, err := operator.Scheme()
	if err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "manifests", "00-crds")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := writeTestCertificate(t)
	addresses := make(chan string, 1)
	deps := AuthnDeps{
		RestConfig: func() (*rest.Config, error) { return cfg, nil },
		Listen: func(network, _ string) (net.Listener, error) {
			listener, err := net.Listen(network, "127.0.0.1:0")
			if err == nil {
				addresses <- listener.Addr().String()
			}
			return listener, err
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- RunAuthnServe(ctx, []string{"--tls-cert", certFile, "--tls-key", keyFile}, deps, &stdout, &stderr)
	}()
	var address string
	select {
	case address = <-addresses:
	case code := <-done:
		t.Fatalf("serve exited %d: %s", code, stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatal("serve did not listen")
	}
	browser := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := browser.Get("https://" + address + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("healthz %d %q %v", resp.StatusCode, body, err)
	}
	var cookieKey corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: release.SystemNamespace, Name: "bedrock-authn-cookie-key"}, &cookieKey); err != nil {
		t.Fatalf("the server must create its cookie key: %v", err)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("shutdown exit %d: %s", code, stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop after the context ended")
	}
}

func TestRootListsAuthn(t *testing.T) {
	if _, ok := Commands()["authn"]; !ok {
		t.Fatal("Commands() must hold authn")
	}
	var usage, ignored bytes.Buffer
	if code := Run([]string{"--help"}, &usage, &ignored); code != 0 || !strings.Contains(usage.String(), "  authn\n") {
		t.Fatalf("usage %q", usage.String())
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"authn"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: bedrock authn") {
		t.Fatalf("no subcommand: exit %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	if code := Run([]string{"authn", "nope"}, &stdout, &stderr); code != 2 || stderr.String() != "unknown authn command: nope\n" {
		t.Fatalf("unknown subcommand: exit %d, stderr %q", code, stderr.String())
	}
}

func writeTestCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bedrock-authn.bedrock-system.svc"},
		DNSNames:     []string{"bedrock-authn.bedrock-system.svc"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func resetEnv(t *testing.T) (client.Client, func(string) (client.Client, error)) {
	t.Helper()
	c, newClient := startEnv(t)
	if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	return c, newClient
}

func createTestUser(t *testing.T, c client.Client, spec v1alpha1.UserSpec) *v1alpha1.User {
	t.Helper()
	name := v1alpha1.UserObjectName(spec.Username)
	user := &v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: name, Labels: map[string]string{v1alpha1.LabelKind: "User", v1alpha1.LabelName: name}}, Spec: spec}
	if err := c.Create(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return user
}

func loginObjects(user, seed string) []client.Object {
	now := metav1.Now()
	expires := metav1.NewTime(now.Add(time.Hour))
	token := &v1alpha1.RefreshToken{
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: secret.SHA256Hex("refresh-" + seed), Labels: map[string]string{v1alpha1.LabelFamily: "family-" + seed}},
		Spec:       v1alpha1.RefreshTokenSpec{Family: "family-" + seed, UserRef: user, ClientID: "bedrock-cli", Scopes: []string{"openid"}, Audience: []string{"bedrock"}, AMR: []string{"pwd"}, AuthTime: now, Session: secret.SHA256Hex("session-" + seed), ExpiresAt: expires},
	}
	session := &v1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: secret.SHA256Hex("session-" + seed)},
		Spec:       v1alpha1.SessionSpec{UserRef: user, AMR: []string{"pwd"}, AuthTime: now, UserAgent: "test", ClientIP: "127.0.0.1", ExpiresAt: expires},
	}
	return []client.Object{token, session}
}

func loginsOf(t *testing.T, c client.Client, user string) int {
	t.Helper()
	ctx := context.Background()
	var tokens v1alpha1.RefreshTokenList
	if err := c.List(ctx, &tokens, client.InNamespace(release.SystemNamespace), client.MatchingFields{"spec.userRef": user}); err != nil {
		t.Fatal(err)
	}
	var sessions v1alpha1.SessionList
	if err := c.List(ctx, &sessions, client.InNamespace(release.SystemNamespace), client.MatchingFields{"spec.userRef": user}); err != nil {
		t.Fatal(err)
	}
	return len(tokens.Items) + len(sessions.Items)
}

func TestResetPassword(t *testing.T) {
	c, newClient := resetEnv(t)
	ctx := context.Background()
	alice := createTestUser(t, c, v1alpha1.UserSpec{Username: "alice", Methods: []string{v1alpha1.MethodPassword}})
	createTestUser(t, c, v1alpha1.UserSpec{Username: "bob", Methods: []string{v1alpha1.MethodPassword}})
	if err := methods.SetPassword(ctx, c, rand.Reader, *alice, "old-password-123"); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(alice), alice); err != nil {
		t.Fatal(err)
	}
	locked := metav1.NewTime(time.Now().Add(time.Hour))
	alice.Status = v1alpha1.UserStatus{LockedUntil: &locked, FailedAttempts: 5, FailureWindowStart: &locked, Locks: 2}
	if err := c.Status().Update(ctx, alice); err != nil {
		t.Fatal(err)
	}
	for _, obj := range append(loginObjects("alice", "a"), loginObjects("bob", "b")...) {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	code := RunResetPassword(ctx, []string{" Alice ", "--kubeconfig", "/unused/admin.conf"}, newClient, rand.Reader, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	match := regexp.MustCompile(`^password: ([0-9A-Za-z]{20})\n$`).FindStringSubmatch(out.String())
	if match == nil {
		t.Fatalf("stdout %q", out.String())
	}
	hash := storedPasswordHash(t, c, "alice")
	if ok, err := secret.Verify(hash, match[1]); err != nil || !ok {
		t.Fatalf("the new password must verify: %v %v", ok, err)
	}
	if ok, _ := secret.Verify(hash, "old-password-123"); ok {
		t.Fatal("the old password must stop working")
	}
	var after v1alpha1.User
	if err := c.Get(ctx, client.ObjectKeyFromObject(alice), &after); err != nil {
		t.Fatal(err)
	}
	if after.Status.LockedUntil != nil || after.Status.FailedAttempts != 0 || after.Status.FailureWindowStart != nil || after.Status.Locks != 0 {
		t.Fatalf("the lock must be cleared: %+v", after.Status)
	}
	if loginsOf(t, c, "alice") != 0 || loginsOf(t, c, "bob") != 2 {
		t.Fatalf("only alice's refresh tokens and sessions go: alice %d bob %d", loginsOf(t, c, "alice"), loginsOf(t, c, "bob"))
	}
}

func TestResetPasswordRefusesLDAPUser(t *testing.T) {
	c, newClient := resetEnv(t)
	createTestUser(t, c, v1alpha1.UserSpec{Username: "t.farahani", Source: "corp-ldap", Methods: []string{v1alpha1.MethodLDAP}})
	var out, errOut bytes.Buffer
	if code := RunResetPassword(context.Background(), []string{"t.farahani"}, newClient, rand.Reader, &out, &errOut); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "user t.farahani comes from corp-ldap") || out.Len() != 0 {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}
	var credential v1alpha1.Credential
	err := c.Get(context.Background(), client.ObjectKey{Namespace: release.SystemNamespace, Name: v1alpha1.CredentialName("t.farahani", v1alpha1.MethodPassword)}, &credential)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("no password may be set for an LDAP user: %v", err)
	}
}

func TestResetPasswordUnknownUser(t *testing.T) {
	_, newClient := resetEnv(t)
	var out, errOut bytes.Buffer
	if code := RunResetPassword(context.Background(), []string{"nobody"}, newClient, rand.Reader, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "user nobody not found") {
		t.Fatalf("exit %d stderr %q", code, errOut.String())
	}
	errOut.Reset()
	if code := RunResetPassword(context.Background(), nil, newClient, rand.Reader, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "reset-password <username>") {
		t.Fatalf("exit %d stderr %q", code, errOut.String())
	}
	errOut.Reset()
	if code := RunResetPassword(context.Background(), []string{"a", "b"}, newClient, rand.Reader, &out, &errOut); code != 2 {
		t.Fatalf("two usernames: exit %d", code)
	}
}
