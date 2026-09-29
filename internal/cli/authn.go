package cli

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/authn/housekeeping"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/server"
	"github.com/cloudyfolks-io/bedrock/internal/operator"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	webhookTokenField = "token"
	housekeepingEvery = time.Minute
	drainTimeout      = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	embeddedUIRoot    = "ui/dist"
	restQPS           = 50
	restBurst         = 100
)

type AuthnDeps struct {
	RestConfig func() (*rest.Config, error)
	Listen     func(network, addr string) (net.Listener, error)
}

type serveOptions struct {
	listen  string
	tlsCert string
	tlsKey  string
	uiDir   string
}

func authnCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: bedrock authn serve [--listen :8443] [--tls-cert /tls/tls.crt] [--tls-key /tls/tls.key] [--ui-dir <dir>]")
		return 2
	}
	switch args[0] {
	case "serve":
		ctrl.SetLogger(zap.New())
		return RunAuthnServe(ctrl.SetupSignalHandler(), args[1:], AuthnDeps{RestConfig: ctrl.GetConfig, Listen: net.Listen}, stdout, stderr)
	case "reset-password":
		return RunResetPassword(context.Background(), args[1:], newClusterClient, rand.Reader, stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown authn command: %s\n", args[0])
	return 2
}

func RunAuthnServe(ctx context.Context, args []string, deps AuthnDeps, stdout, stderr io.Writer) int {
	options, err := parseServeFlags(args, stderr)
	if err != nil {
		return 2
	}
	certificate := certificateFiles(options.tlsCert, options.tlsKey)
	if _, err := certificate(nil); err != nil {
		return fail(stderr, fmt.Errorf("tls: %w", err))
	}
	cfg, err := deps.RestConfig()
	if err != nil {
		return fail(stderr, err)
	}
	tuned := tunedRestConfig(cfg)
	scheme, err := operator.Scheme()
	if err != nil {
		return fail(stderr, err)
	}
	c, err := client.New(tuned, client.Options{Scheme: scheme})
	if err != nil {
		return fail(stderr, err)
	}
	ui, err := uiFiles(options.uiDir)
	if err != nil {
		return fail(stderr, err)
	}
	handler, err := server.New(ctx, server.Config{
		RestConfig:    tuned,
		Random:        rand.Reader,
		Clock:         time.Now,
		UI:            ui,
		WebhookSecret: webhookToken(c),
	})
	if err != nil {
		return fail(stderr, err)
	}
	listener, err := deps.Listen("tcp", options.listen)
	if err != nil {
		return fail(stderr, err)
	}
	background := make(chan error, 1)
	go func() { background <- housekeeping.Run(ctx, tuned, c, podIdentity(), housekeepingEvery) }()
	fmt.Fprintf(stdout, "authn serving on %s\n", listener.Addr())
	return serveTLS(ctx, listener, handler, certificate, background, stderr)
}

func parseServeFlags(args []string, stderr io.Writer) (serveOptions, error) {
	flags := flag.NewFlagSet("authn serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", ":8443", "address to serve HTTPS on")
	tlsCert := flags.String("tls-cert", "/tls/tls.crt", "TLS certificate file")
	tlsKey := flags.String("tls-key", "/tls/tls.key", "TLS private key file")
	uiDir := flags.String("ui-dir", "", "directory of a built login UI to serve instead of the embedded one")
	if err := flags.Parse(args); err != nil {
		return serveOptions{}, err
	}
	if flags.NArg() > 0 {
		err := fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
		fmt.Fprintln(stderr, err)
		return serveOptions{}, err
	}
	return serveOptions{listen: *listen, tlsCert: *tlsCert, tlsKey: *tlsKey, uiDir: *uiDir}, nil
}

func serveTLS(ctx context.Context, listener net.Listener, handler http.Handler, certificate func(*tls.ClientHelloInfo) (*tls.Certificate, error), background <-chan error, stderr io.Writer) int {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certificate},
	}
	served := make(chan error, 1)
	go func() { served <- srv.ServeTLS(listener, "", "") }()
	stopped := waitForStop(ctx, served, background)
	drain, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	if err := srv.Shutdown(drain); err != nil {
		return fail(stderr, err)
	}
	if stopped != nil {
		return fail(stderr, stopped)
	}
	return 0
}

func waitForStop(ctx context.Context, served, background <-chan error) error {
	select {
	case err := <-served:
		return err
	case err := <-background:
		return housekeepingFailure(err)
	case <-ctx.Done():
		return nil
	}
}

func housekeepingFailure(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("housekeeping: %w", err)
}

func certificateFiles(certFile, keyFile string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		return &pair, nil
	}
}

func uiFiles(dir string) (fs.FS, error) {
	if dir != "" {
		return os.DirFS(dir), nil
	}
	return fs.Sub(server.UI, embeddedUIRoot)
}

func webhookToken(reader client.Reader) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var stored corev1.Secret
		if err := reader.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: apiserver.TokenSecretName}, &stored); err != nil {
			return "", err
		}
		return string(stored.Data[webhookTokenField]), nil
	}
}

func podIdentity() string {
	if name := os.Getenv("POD_NAME"); name != "" {
		return name
	}
	host, err := os.Hostname()
	if err != nil {
		return "bedrock-authn"
	}
	return host
}

func tunedRestConfig(cfg *rest.Config) *rest.Config {
	tuned := rest.CopyConfig(cfg)
	tuned.QPS = restQPS
	tuned.Burst = restBurst
	return tuned
}

const resetPasswordUsage = "usage: bedrock authn reset-password <username> [--kubeconfig PATH]"

func RunResetPassword(ctx context.Context, args []string, newClient func(string) (client.Client, error), random io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("authn reset-password", flag.ContinueOnError)
	flags.SetOutput(stderr)
	kubeconfig := flags.String("kubeconfig", "/var/lib/k0s/pki/admin.conf", "admin kubeconfig")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() == 0 {
		fmt.Fprintln(stderr, resetPasswordUsage)
		return 2
	}
	raw := flags.Arg(0)
	if err := flags.Parse(flags.Args()[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, resetPasswordUsage)
		return 2
	}
	username, err := v1alpha1.NormalizeUsername(raw)
	if err != nil {
		return fail(stderr, err)
	}
	c, err := newClient(*kubeconfig)
	if err != nil {
		return fail(stderr, err)
	}
	password, err := resetPassword(ctx, c, random, username)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "password: %s\n", password)
	return 0
}

func resetPassword(ctx context.Context, c client.Client, random io.Reader, username string) (string, error) {
	var user v1alpha1.User
	err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: v1alpha1.UserObjectName(username)}, &user)
	if apierrors.IsNotFound(err) {
		return "", fmt.Errorf("user %s not found", username)
	}
	if err != nil {
		return "", err
	}
	if user.Spec.Source != "" {
		return "", fmt.Errorf("user %s comes from %s, change the password there", username, user.Spec.Source)
	}
	password, err := generatedPassword(random)
	if err != nil {
		return "", err
	}
	if err := methods.SetPassword(ctx, c, random, user, password); err != nil {
		return "", err
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.User
		if err := c.Get(ctx, client.ObjectKeyFromObject(&user), &current); err != nil {
			return err
		}
		current.Status = unlocked(current.Status)
		return c.Status().Update(ctx, &current, client.FieldOwner(v1alpha1.AuthnFieldManager))
	}); err != nil {
		return "", err
	}
	if err := revokeLogins(ctx, c, user.Name); err != nil {
		return "", err
	}
	return password, nil
}

func unlocked(status v1alpha1.UserStatus) v1alpha1.UserStatus {
	next := *status.DeepCopy()
	next.LockedUntil = nil
	next.FailureWindowStart = nil
	next.FailedAttempts = 0
	next.Locks = 0
	return next
}

func revokeLogins(ctx context.Context, c client.Client, user string) error {
	byUser := []client.ListOption{client.InNamespace(release.SystemNamespace), client.MatchingFields{"spec.userRef": user}}
	var tokens v1alpha1.RefreshTokenList
	if err := c.List(ctx, &tokens, byUser...); err != nil {
		return err
	}
	for i := range tokens.Items {
		if err := client.IgnoreNotFound(c.Delete(ctx, &tokens.Items[i])); err != nil {
			return err
		}
	}
	var sessions v1alpha1.SessionList
	if err := c.List(ctx, &sessions, byUser...); err != nil {
		return err
	}
	for i := range sessions.Items {
		if err := client.IgnoreNotFound(c.Delete(ctx, &sessions.Items[i])); err != nil {
			return err
		}
	}
	return nil
}
