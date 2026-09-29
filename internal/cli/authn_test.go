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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

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
