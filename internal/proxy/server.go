package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	loginsPerMinute  = 30
	secretCacheLife  = time.Minute
	readyProbeSecret = "readyz-probe"
	clientSecretKey  = "clientSecret"
)

type Config struct {
	RestConfig      *rest.Config
	Random          io.Reader
	Clock           func() time.Time
	Panel           fs.FS
	Subresources    *Subresources
	AuthnAddress    string
	AuthnServerName string
	AuthnCA         []byte
	PrometheusURL   *url.URL
	PodName         string
}

type secretCache struct {
	reader client.Reader
	clock  func() time.Time
	mu     sync.Mutex
	value  string
	readAt time.Time
}

func (s *secretCache) get(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if s.value != "" && now.Sub(s.readAt) < secretCacheLife {
		return s.value, nil
	}
	var stored corev1.Secret
	if err := s.reader.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: ConsoleSecretName}, &stored); err != nil {
		return "", err
	}
	s.value, s.readAt = string(stored.Data[clientSecretKey]), now
	return s.value, nil
}

func New(ctx context.Context, cfg Config) (http.Handler, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	privileged, err := client.New(cfg.RestConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	upstream, err := NewUpstream(cfg.RestConfig)
	if err != nil {
		return nil, err
	}
	host := NewHostSource(privileged, cfg.Clock).Host
	store := NewStore(privileged, cfg.Clock)
	oidc := NewOIDC(OIDCConfig{
		Host:            host,
		ClientSecret:    (&secretCache{reader: privileged, clock: cfg.Clock}).get,
		AuthnAddress:    cfg.AuthnAddress,
		AuthnServerName: cfg.AuthnServerName,
		AuthnCA:         cfg.AuthnCA,
		Clock:           cfg.Clock,
	})
	resolver := NewResolver(store, oidc, privileged, cfg.Clock, cfg.PodName)
	start, callback, logout := LoginHandlers(LoginDeps{
		Store: store, OIDC: oidc, Reader: privileged, Random: cfg.Random, Clock: cfg.Clock, Host: host,
		Limiter: methods.NewRateLimiter(loginsPerMinute, time.Minute), Callbacks: methods.NewRateLimiter(loginsPerMinute, time.Minute),
		Evict: resolver.Forget,
	})
	if cfg.Subresources == nil {
		cfg.Subresources = NewSubresources()
	}
	parts := serverParts{
		ctx: ctx, cfg: cfg, host: host, upstream: upstream, privileged: privileged, resolver: resolver,
		start: start, callback: callback, logout: logout,
	}
	routes := Routes{Console: parts.console(), API: parts.api(), Health: health(privileged)}
	return requestLog(Dispatch(host, routes)), nil
}

type serverParts struct {
	ctx        context.Context
	cfg        Config
	host       func(context.Context) (string, error)
	upstream   *Upstream
	privileged client.Client
	resolver   *Resolver
	start      http.Handler
	callback   http.Handler
	logout     http.Handler
}

func (p serverParts) cookieChain(next http.Handler) http.Handler {
	return Authenticate(CookieAuth(p.resolver), RequireOrigin(p.host, RequireCSRF(next)))
}

func (p serverParts) kubernetes(passThrough http.Handler) http.Handler {
	return SubresourceRouter(p.cfg.Subresources, p.upstream, p.privileged, passThrough)
}

func (p serverParts) console() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET "+PathLogin, p.start)
	mux.Handle("GET "+PathCallback, p.callback)
	mux.Handle("POST "+PathLogout, p.cookieChain(p.logout))
	mux.Handle("GET "+PathSession, Authenticate(CookieAuth(p.resolver), SessionHandler(p.host)))
	mux.Handle("GET "+PathStream, Authenticate(CookieAuth(p.resolver), RequireOrigin(p.host, StreamHandler(p.upstream, p.resolver, StreamOptions{Context: p.ctx, Clock: p.cfg.Clock}))))
	mux.Handle("POST "+PathAuthz, p.cookieChain(AuthzHandler(p.upstream)))
	mux.Handle("GET "+PathProjects, p.cookieChain(ProjectsHandler(p.upstream, p.privileged)))
	mux.Handle("GET "+PathMetrics, p.cookieChain(MetricsHandler(p.upstream, p.cfg.PrometheusURL, p.cfg.Clock)))
	mux.Handle("GET "+PathKubeconfig, KubeconfigHandler(p.privileged, p.host))
	mux.Handle("GET "+PathCA, CAHandler(p.privileged))
	kube := p.cookieChain(p.kubernetes(ConsolePassThrough(p.upstream)))
	for _, pattern := range kubernetesPatterns() {
		mux.Handle(pattern, kube)
	}
	mux.Handle("/", PanelHandler(p.cfg.Panel, p.host))
	return RefuseCrossSite(mux)
}

func (p serverParts) api() http.Handler {
	mux := http.NewServeMux()
	bearer := func(next http.Handler) http.Handler { return Authenticate(BearerAuth(), next) }
	mux.Handle("POST "+PathAuthz, bearer(AuthzHandler(p.upstream)))
	mux.Handle("GET "+PathProjects, bearer(ProjectsHandler(p.upstream, p.privileged)))
	mux.Handle("GET "+PathMetrics, bearer(MetricsHandler(p.upstream, p.cfg.PrometheusURL, p.cfg.Clock)))
	mux.Handle("GET "+PathKubeconfig, KubeconfigHandler(p.privileged, p.host))
	mux.Handle("GET "+PathCA, CAHandler(p.privileged))
	kube := bearer(p.kubernetes(APIPassThrough(p.upstream)))
	for _, pattern := range kubernetesPatterns() {
		mux.Handle(pattern, kube)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found")
	})
	return mux
}

func kubernetesPatterns() []string {
	return []string{"/api", "/api/", "/apis", "/apis/", "/openapi/", "/version"}
}

func health(reader client.Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathReadyz {
			var probe corev1.Secret
			err := reader.Get(r.Context(), client.ObjectKey{Namespace: StoreNamespace, Name: readyProbeSecret}, &probe)
			if err != nil && !apierrors.IsNotFound(err) {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		_, _ = w.Write([]byte("ok"))
	})
}

type userNote struct {
	mu   sync.Mutex
	name string
}

type userNoteKey struct{}

func noteUser(ctx context.Context, name string) {
	if note, ok := ctx.Value(userNoteKey{}).(*userNote); ok {
		note.mu.Lock()
		note.name = name
		note.mu.Unlock()
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	_ = http.NewResponseController(s.ResponseWriter).Flush()
}

func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if s.status == 0 {
		s.status = http.StatusSwitchingProtocols
	}
	return http.NewResponseController(s.ResponseWriter).Hijack()
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		note := &userNote{}
		wrapped := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r.WithContext(context.WithValue(r.Context(), userNoteKey{}, note)))
		note.mu.Lock()
		defer note.mu.Unlock()
		slog.InfoContext(r.Context(), "proxy request", "method", r.Method, "path", r.URL.Path, "status", wrapped.status, "duration", time.Since(started), "user", note.name)
	})
}
