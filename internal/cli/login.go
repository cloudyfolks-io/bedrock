package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/cliauth"
)

type LoginDeps struct {
	Home string
	HTTP func(caFile string) (*http.Client, error)
	Open func(url string) error
	Now  func() time.Time
}

func loginCommand(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	deps := LoginDeps{Home: home, HTTP: httpClientWithCA, Open: openBrowser, Now: time.Now}
	return RunLogin(context.Background(), args, deps, stdout, stderr)
}

type flowFunc func(ctx context.Context, c *http.Client, issuer, clientID string) (cliauth.Tokens, error)

func deviceFlow(stderr io.Writer) flowFunc {
	return func(ctx context.Context, c *http.Client, issuer, clientID string) (cliauth.Tokens, error) {
		return cliauth.DeviceLogin(ctx, c, issuer, clientID, func(uri, code string) {
			fmt.Fprintf(stderr, "open %s\n", uri)
			fmt.Fprintf(stderr, "code %s\n", code)
		})
	}
}

func loopbackFlow(deps LoginDeps) flowFunc {
	return func(ctx context.Context, c *http.Client, issuer, clientID string) (cliauth.Tokens, error) {
		return cliauth.LoopbackLogin(ctx, c, issuer, clientID, deps.Open)
	}
}

func loadTokens(ctx context.Context, c *http.Client, deps LoginDeps, issuer, clientID string, flow flowFunc) (cliauth.Tokens, error) {
	path := cliauth.CachePath(deps.Home, issuer)
	now := deps.Now()
	cached, err := cliauth.ReadCache(path)
	if err == nil && cliauth.Fresh(cached, now) {
		return cached, nil
	}
	if err == nil && cached.RefreshToken != "" {
		if refreshed, rerr := cliauth.Refresh(ctx, c, issuer, clientID, cached.RefreshToken); rerr == nil {
			return refreshed, nil
		}
	}
	return flow(ctx, c, issuer, clientID)
}

func RunLogin(ctx context.Context, args []string, deps LoginDeps, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "issuer URL, e.g. https://sso.example.com")
	clientID := flags.String("client-id", "bedrock-cli", "OAuth client ID")
	browser := flags.Bool("browser", false, "use the loopback authorization code flow instead of the device flow")
	caFile := flags.String("ca-file", "", "additional CA certificate for the authn server")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *server == "" {
		fmt.Fprintln(stderr, "--server is required")
		return 2
	}
	client, err := deps.HTTP(*caFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	flow := deviceFlow(stderr)
	if *browser {
		flow = loopbackFlow(deps)
	}
	tokens, err := loadTokens(ctx, client, deps, *server, *clientID, flow)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := cliauth.WriteCache(cliauth.CachePath(deps.Home, *server), tokens); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in as %s\n", subjectOf(tokens.IDToken))
	return 0
}

func subjectOf(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Subject
}

func httpClientWithCA(caFile string) (*http.Client, error) {
	if caFile == "" {
		return http.DefaultClient, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in %s", caFile)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}, nil
}

func openBrowser(url string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}
