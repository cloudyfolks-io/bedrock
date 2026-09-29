package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"golang.org/x/text/language"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/account"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/authn/store"
	"github.com/cloudyfolks-io/bedrock/internal/authn/webhook"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	pathAuthorize            = "/oauth/v2/authorize"
	pathAuthorizeDone        = pathAuthorize + "/callback"
	pathToken                = "/oauth/v2/token"
	pathDevice               = "/oauth/v2/device_authorization"
	pathIntrospect           = "/oauth/v2/introspect"
	pathRevoke               = "/oauth/v2/revoke"
	pathKeys                 = "/oauth/v2/keys"
	pathEndSession           = "/oidc/v1/end_session"
	pathUserinfo             = "/oidc/v1/userinfo"
	pathWebhook              = "/webhook/v1/tokenreview"
	pathHealthz              = "/healthz"
	pathReadyz               = "/readyz"
	pathCluster              = "/api/v1/cluster"
	pathLogin                = "/api/v1/login/"
	pathLoginChallenge       = pathLogin + "challenge"
	pathLoginAnswer          = pathLogin + "answer"
	pathAccount              = "/api/v1/account"
	pathAccountPassword      = pathAccount + "/password"
	pathAccountTOTPVerify    = pathAccount + "/totp/verify"
	pathAccountRecoveryCodes = pathAccount + "/recovery-codes"

	cookieKeySecret   = "bedrock-authn-cookie-key"
	cookieKeyField    = "key"
	cookieKeyLength   = 32
	keysTTL           = 30 * time.Second
	attemptsPerMinute = 10
	upstreamTimeout   = 15 * time.Second
	clusterCAConfig   = "kube-root-ca.crt"
	apiServerPort     = "6443"
	crossSite         = "cross-site"
)

var errCAMissing = errors.New("the cluster CA bundle is empty")

type Config struct {
	RestConfig    *rest.Config
	Random        io.Reader
	Clock         func() time.Time
	UI            fs.FS
	WebhookSecret func(context.Context) (string, error)
}

type keyCache struct {
	reader client.Reader
	clock  func() time.Time
	mu     sync.Mutex
	loaded time.Time
	keys   []keys.Key
}

func (k *keyCache) load(ctx context.Context) ([]keys.Key, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.clock()
	if !k.loaded.IsZero() && now.Sub(k.loaded) < keysTTL {
		return k.keys, nil
	}
	loaded, err := keys.Load(ctx, k.reader)
	if err != nil {
		return nil, err
	}
	k.keys = loaded
	k.loaded = now
	return loaded, nil
}

func New(ctx context.Context, cfg Config) (http.Handler, error) {
	direct, err := uncachedClient(cfg.RestConfig)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	cryptoKey, err := cookieKey(ctx, direct, cfg.Random)
	if err != nil {
		return nil, fmt.Errorf("cookie key: %w", err)
	}
	settings := func(ctx context.Context) (policy.Settings, error) { return policy.ReadSettings(ctx, direct) }
	host := hostOf(settings)
	limiter := methods.NewRateLimiter(attemptsPerMinute, time.Minute)
	st := store.New(store.Config{
		Client:   direct,
		Reader:   direct,
		Random:   cfg.Random,
		Clock:    cfg.Clock,
		Settings: settings,
		Keys:     (&keyCache{reader: direct, clock: cfg.Clock}).load,
	})
	registry := methods.NewRegistry(
		methods.NewPassword(direct, cfg.Random, settings),
		methods.NewTOTP(direct, cfg.Random, issuerOf(settings)),
		methods.NewRecovery(direct, cfg.Random),
		methods.NewLDAP(direct, methods.DialLDAP),
		methods.NewOIDC(direct, methods.DefaultRelyingParties(upstreamClient, providerSecret(direct))),
	)
	provider, err := op.NewProvider(providerConfig(cryptoKey), st, op.IssuerFromHost(""), endpointOptions()...)
	if err != nil {
		return nil, err
	}
	issuerFromRequest, err := op.IssuerFromHost("")(false)
	if err != nil {
		return nil, err
	}
	withIssuer := op.NewIssuerInterceptor(issuerFromRequest).Handler
	credentials := newCredentialGate(credentialGateSlots, credentialGateWait)
	loginHandler := refuseCrossSite(withIssuer(credentials.middleware(isLoginCredentialCheck, login.Handler(login.Deps{
		Store:    st,
		Client:   direct,
		Methods:  registry,
		Settings: settings,
		Random:   cfg.Random,
		Clock:    cfg.Clock,
		Limiter:  limiter,
		Callback: op.AuthCallbackURL(provider),
		LDAPDial: methods.DialLDAP,
	}))))
	accountHandler := refuseCrossSite(withIssuer(credentials.middleware(isAccountCredentialCheck, account.Handler(account.Deps{
		Store:    st,
		Client:   direct,
		Methods:  registry,
		Settings: settings,
		Random:   cfg.Random,
		Clock:    cfg.Clock,
		Limiter:  limiter,
	}))))
	mux := http.NewServeMux()
	mux.Handle(pathLogin, loginHandler)
	mux.Handle(pathAccount, accountHandler)
	mux.Handle(pathAccount+"/", accountHandler)
	mux.Handle(pathWebhook, webhook.Handler(direct, direct, cfg.Clock, cfg.WebhookSecret))
	mux.Handle("GET "+pathCluster, ClusterHandler(direct, host))
	mux.Handle(uiPrefix, UIHandler(cfg.UI))
	mux.HandleFunc("GET "+pathHealthz, healthz)
	mux.Handle("GET "+pathReadyz, readyz(st))
	mux.Handle(pathAuthorizeDone, sessionBound(direct, provider))
	mux.Handle("/", provider)
	return HostGuard(host, mux), nil
}

func ClusterHandler(reader client.Reader, host func(context.Context) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current, err := host(r.Context())
		if err != nil {
			unavailable(w, r, "settings_unavailable", err)
			return
		}
		var ca corev1.ConfigMap
		if err := reader.Get(r.Context(), client.ObjectKey{Namespace: release.SystemNamespace, Name: clusterCAConfig}, &ca); err != nil {
			unavailable(w, r, "cluster_ca_unavailable", err)
			return
		}
		if ca.Data["ca.crt"] == "" {
			unavailable(w, r, "cluster_ca_unavailable", errCAMissing)
			return
		}
		httpjson.Write(w, http.StatusOK, map[string]string{
			"server":               "https://api." + current + ":" + apiServerPort,
			"certificateAuthority": ca.Data["ca.crt"],
		})
	})
}

func sessionBound(reader client.Reader, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := op.ParseAuthorizeCallbackRequest(r)
		if err != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		var request v1alpha1.AuthRequest
		err = reader.Get(r.Context(), client.ObjectKey{Namespace: release.SystemNamespace, Name: id}, &request)
		switch {
		case apierrors.IsNotFound(err):
			httpjson.WriteError(w, http.StatusUnauthorized, "no_session")
		case err != nil:
			slog.ErrorContext(r.Context(), "authorize callback failed", "error", err)
			httpjson.WriteError(w, http.StatusInternalServerError, "internal")
		case !sessionMatches(request.Status.Session, r):
			httpjson.WriteError(w, http.StatusUnauthorized, "no_session")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func sessionMatches(hash string, r *http.Request) bool {
	cookie, err := r.Cookie(login.CookieSession)
	return err == nil && hash != "" && cookie.Value != "" && secret.Equal(hash, secret.SHA256Hex(cookie.Value))
}

func refuseCrossSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Fetch-Site") == crossSite && changesState(r) {
			httpjson.WriteError(w, http.StatusForbidden, "csrf")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func changesState(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return r.URL.Path == pathLoginChallenge
	}
	return true
}

func providerConfig(key [cookieKeyLength]byte) *op.Config {
	return &op.Config{
		CryptoKey:              key,
		CodeMethodS256:         true,
		AuthMethodPost:         true,
		GrantTypeRefreshToken:  true,
		RequestObjectSupported: false,
		SupportedScopes:        []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail, "groups", oidc.ScopeOfflineAccess},
		SupportedClaims:        append(slices.Clone(op.DefaultSupportedClaims), "groups", "sid"),
		DeviceAuthorization:    store.DeviceConfig(),
		SupportedUILocales:     []language.Tag{language.English, language.Persian, language.Arabic},
	}
}

func endpointOptions() []op.Option {
	return []op.Option{
		op.WithCustomEndpoints(
			op.NewEndpoint(pathAuthorize),
			op.NewEndpoint(pathToken),
			op.NewEndpoint(pathUserinfo),
			op.NewEndpoint(pathRevoke),
			op.NewEndpoint(pathEndSession),
			op.NewEndpoint(pathKeys),
		),
		op.WithCustomIntrospectionEndpoint(op.NewEndpoint(pathIntrospect)),
		op.WithCustomDeviceAuthorizationEndpoint(op.NewEndpoint(pathDevice)),
	}
}

func uncachedClient(cfg *rest.Config) (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

func cookieKey(ctx context.Context, c client.Client, random io.Reader) ([cookieKeyLength]byte, error) {
	fresh := make([]byte, cookieKeyLength)
	if _, err := io.ReadFull(random, fresh); err != nil {
		return [cookieKeyLength]byte{}, err
	}
	if err := c.Create(ctx, cookieKeyObject(fresh), client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil && !apierrors.IsAlreadyExists(err) {
		return [cookieKeyLength]byte{}, err
	}
	var stored corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: cookieKeySecret}, &stored); err != nil {
		return [cookieKeyLength]byte{}, err
	}
	value := stored.Data[cookieKeyField]
	if len(value) != cookieKeyLength {
		return [cookieKeyLength]byte{}, fmt.Errorf("secret %s/%s holds %d bytes, want %d", release.SystemNamespace, cookieKeySecret, len(value), cookieKeyLength)
	}
	return [cookieKeyLength]byte(value), nil
}

func cookieKeyObject(value []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cookieKeySecret,
			Namespace: release.SystemNamespace,
			Labels: map[string]string{
				v1alpha1.LabelAuthn: "true",
				v1alpha1.LabelKind:  "CookieKey",
				v1alpha1.LabelName:  cookieKeySecret,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{cookieKeyField: value},
	}
}

func issuerOf(settings func(context.Context) (policy.Settings, error)) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		current, err := settings(ctx)
		if err != nil {
			return "", err
		}
		return policy.Issuer(current), nil
	}
}

func hostOf(settings func(context.Context) (policy.Settings, error)) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		current, err := settings(ctx)
		if err != nil {
			return "", err
		}
		return current.Host, nil
	}
}

func upstreamClient(caBundle []byte) *http.Client {
	return &http.Client{
		Timeout: upstreamTimeout,
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: rootsWith(caBundle)},
		},
	}
}

func rootsWith(caBundle []byte) *x509.CertPool {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	roots.AppendCertsFromPEM(caBundle)
	return roots
}

func providerSecret(reader client.Reader) func(context.Context, v1alpha1.IdentityProvider) (string, error) {
	return func(ctx context.Context, provider v1alpha1.IdentityProvider) (string, error) {
		var stored corev1.Secret
		if err := reader.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: provider.Spec.SecretRef}, &stored); err != nil {
			return "", err
		}
		return string(stored.Data["clientSecret"]), nil
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")
}

func readyz(st *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := st.Health(r.Context()); err != nil {
			slog.WarnContext(r.Context(), "authn server not ready", "error", err)
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
}
