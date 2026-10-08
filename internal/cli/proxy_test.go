package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestProxyServeFlags(t *testing.T) {
	defaults, err := parseProxyFlags(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	wantDefaults := proxyOptions{
		listen: ":8443", tlsCert: "/tls/tls.crt", tlsKey: "/tls/tls.key",
		authnAddress: "bedrock-authn.bedrock-system.svc:443", authnServerName: "bedrock-authn.bedrock-system.svc",
		authnCA: "/authn-ca/ca.crt", prometheusURL: "http://prometheus-operated.monitoring.svc:9090",
	}
	if defaults != wantDefaults {
		t.Fatalf("defaults %+v", defaults)
	}
	custom, err := parseProxyFlags([]string{
		"--listen", "127.0.0.1:9443", "--tls-cert", "/a.crt", "--tls-key", "/a.key", "--authn-address", "127.0.0.1:1",
		"--authn-server-name", "authn.test", "--authn-ca", "/ca.pem", "--prometheus-url", "http://prom:9090", "--panel-dir", "/srv/panel",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	wantCustom := proxyOptions{
		listen: "127.0.0.1:9443", tlsCert: "/a.crt", tlsKey: "/a.key", authnAddress: "127.0.0.1:1", authnServerName: "authn.test",
		authnCA: "/ca.pem", prometheusURL: "http://prom:9090", panelDir: "/srv/panel",
	}
	if custom != wantCustom {
		t.Fatalf("custom %+v", custom)
	}
	for _, args := range [][]string{{"--nope"}, {"extra"}} {
		if _, err := parseProxyFlags(args, io.Discard); err == nil {
			t.Fatalf("args %v must fail", args)
		}
	}
}

func TestProxyServeFailsWithoutTLSFiles(t *testing.T) {
	untouched := ProxyDeps{
		RestConfig: func() (*rest.Config, error) { return nil, errors.New("the API must not be reached") },
		Listen:     func(string, string) (net.Listener, error) { return nil, errors.New("nothing may listen") },
	}
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "missing")
	code := RunProxyServe(context.Background(), []string{"--tls-cert", missing + ".crt", "--tls-key", missing + ".key"}, untouched, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "tls:") || strings.Contains(stderr.String(), "the API must not be reached") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	if code := RunProxyServe(context.Background(), []string{"--nope"}, untouched, &stdout, &stderr); code != 2 {
		t.Fatalf("bad flag: exit %d", code)
	}
}

func TestProxyCommandNeedsServe(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := proxyCommand(nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: bedrock proxy serve") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}
