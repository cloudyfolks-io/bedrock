package cli

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/cloudyfolks-io/bedrock/internal/operator"
	"github.com/cloudyfolks-io/bedrock/internal/proxy"
)

const (
	proxyUsage        = "usage: bedrock proxy serve [--listen :8443] [--tls-cert /tls/tls.crt] [--tls-key /tls/tls.key] [--authn-address host:port] [--authn-server-name name] [--authn-ca /authn-ca/ca.crt] [--prometheus-url url] [--panel-dir <dir>]"
	proxySweepEvery   = time.Minute
	embeddedPanelRoot = "ui/dist"
)

type ProxyDeps struct {
	RestConfig func() (*rest.Config, error)
	Listen     func(network, addr string) (net.Listener, error)
}

type proxyOptions struct {
	listen          string
	tlsCert         string
	tlsKey          string
	authnAddress    string
	authnServerName string
	authnCA         string
	prometheusURL   string
	panelDir        string
}

func proxyCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(stderr, proxyUsage)
		return 2
	}
	ctrl.SetLogger(zap.New())
	return RunProxyServe(ctrl.SetupSignalHandler(), args[1:], ProxyDeps{RestConfig: ctrl.GetConfig, Listen: net.Listen}, stdout, stderr)
}

func RunProxyServe(ctx context.Context, args []string, deps ProxyDeps, stdout, stderr io.Writer) int {
	options, err := parseProxyFlags(args, stderr)
	if err != nil {
		return 2
	}
	certificate := certificateFiles(options.tlsCert, options.tlsKey)
	if _, err := certificate(nil); err != nil {
		return fail(stderr, fmt.Errorf("tls: %w", err))
	}
	authnCA, err := os.ReadFile(options.authnCA)
	if err != nil {
		return fail(stderr, fmt.Errorf("authn ca: %w", err))
	}
	prometheus, err := url.Parse(options.prometheusURL)
	if err != nil {
		return fail(stderr, fmt.Errorf("prometheus url: %w", err))
	}
	panel, err := panelFiles(options.panelDir)
	if err != nil {
		return fail(stderr, err)
	}
	subresources, err := registeredSubresources()
	if err != nil {
		return fail(stderr, err)
	}
	cfg, err := deps.RestConfig()
	if err != nil {
		return fail(stderr, err)
	}
	tuned := tunedRestConfig(cfg)
	handler, err := proxy.New(ctx, proxy.Config{
		RestConfig: tuned, Random: rand.Reader, Clock: time.Now, Panel: panel, Subresources: subresources,
		AuthnAddress: options.authnAddress, AuthnServerName: options.authnServerName, AuthnCA: authnCA,
		PrometheusURL: prometheus, PodName: podIdentity(),
	})
	if err != nil {
		return fail(stderr, err)
	}
	store, err := proxyStore(tuned)
	if err != nil {
		return fail(stderr, err)
	}
	listener, err := deps.Listen("tcp", options.listen)
	if err != nil {
		return fail(stderr, err)
	}
	background := make(chan error, 1)
	go func() { background <- proxy.Sweeper(ctx, store, proxySweepEvery) }()
	fmt.Fprintf(stdout, "proxy serving on %s\n", listener.Addr())
	return serveTLS(ctx, listener, handler, certificate, background, stderr)
}

func parseProxyFlags(args []string, stderr io.Writer) (proxyOptions, error) {
	flags := flag.NewFlagSet("proxy serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", ":8443", "address to serve HTTPS on")
	tlsCert := flags.String("tls-cert", "/tls/tls.crt", "TLS certificate file")
	tlsKey := flags.String("tls-key", "/tls/tls.key", "TLS private key file")
	authnAddress := flags.String("authn-address", "bedrock-authn.bedrock-system.svc:443", "in-cluster address of bedrock-authn")
	authnServerName := flags.String("authn-server-name", "bedrock-authn.bedrock-system.svc", "TLS server name of bedrock-authn")
	authnCA := flags.String("authn-ca", "/authn-ca/ca.crt", "CA file that signs the bedrock-authn certificate")
	prometheusURL := flags.String("prometheus-url", "http://prometheus-operated.monitoring.svc:9090", "Prometheus base URL")
	panelDir := flags.String("panel-dir", "", "directory of a built panel to serve instead of the embedded one")
	if err := flags.Parse(args); err != nil {
		return proxyOptions{}, err
	}
	if flags.NArg() > 0 {
		err := fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
		fmt.Fprintln(stderr, err)
		return proxyOptions{}, err
	}
	return proxyOptions{
		listen: *listen, tlsCert: *tlsCert, tlsKey: *tlsKey, authnAddress: *authnAddress, authnServerName: *authnServerName,
		authnCA: *authnCA, prometheusURL: *prometheusURL, panelDir: *panelDir,
	}, nil
}

func panelFiles(dir string) (fs.FS, error) {
	if dir != "" {
		return os.DirFS(dir), nil
	}
	return fs.Sub(proxy.UI, embeddedPanelRoot)
}

func registeredSubresources() (*proxy.Subresources, error) {
	registry := proxy.NewSubresources()
	for _, register := range subresourceRegistrars() {
		if err := register(registry); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func proxyStore(cfg *rest.Config) (*proxy.Store, error) {
	scheme, err := operator.Scheme()
	if err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	return proxy.NewStore(c, time.Now), nil
}
