package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/zitadel/oidc/v3/pkg/client/rp"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"golang.org/x/oauth2"

	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

const (
	oidcScopes       = "openid profile email groups offline_access"
	oidcTimeout      = 15 * time.Second
	pkceVerifierSize = 32
)

var (
	ErrInvalidGrant = errors.New("invalid_grant")
	errBadIDToken   = errors.New("id token rejected")
)

type Identity struct {
	Subject     string
	Name        string
	Email       string
	Groups      []string
	SessionName string
}

type Tokens struct {
	Access       string
	Refresh      string
	AccessExpiry time.Time
	Identity     Identity
}

type OIDCConfig struct {
	Host            func(context.Context) (string, error)
	ClientSecret    func(context.Context) (string, error)
	AuthnAddress    string
	AuthnServerName string
	AuthnCA         []byte
	Clock           func() time.Time
}

type partyKey struct {
	host   string
	secret string
}

type OIDC struct {
	cfg     OIDCConfig
	client  *http.Client
	mu      sync.Mutex
	parties map[partyKey]rp.RelyingParty
}

func NewOIDC(cfg OIDCConfig) *OIDC {
	return &OIDC{cfg: cfg, client: authnHTTPClient(cfg), parties: map[partyKey]rp.RelyingParty{}}
}

func PKCE(random io.Reader) (string, string, error) {
	raw := make([]byte, pkceVerifierSize)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	return verifier, pkceChallenge(verifier), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (o *OIDC) AuthorizeURL(ctx context.Context, state, nonce, challenge string) (string, error) {
	party, err := o.party(ctx)
	if err != nil {
		return "", err
	}
	return rp.AuthURL(state, party, rp.WithCodeChallenge(challenge), withNonceParam(nonce)), nil
}

func (o *OIDC) Exchange(ctx context.Context, code, verifier, nonce string) (Tokens, error) {
	party, err := o.party(ctx)
	if err != nil {
		return Tokens{}, err
	}
	exchanged, err := rp.CodeExchange[*oidc.IDTokenClaims](ctx, code, party, rp.WithCodeVerifier(verifier))
	if err != nil {
		return Tokens{}, fmt.Errorf("code exchange: %w", err)
	}
	if exchanged.IDTokenClaims == nil || !secret.Equal(exchanged.IDTokenClaims.Nonce, nonce) {
		return Tokens{}, fmt.Errorf("%w: nonce", errBadIDToken)
	}
	identity := identityOf(exchanged.IDTokenClaims)
	if identity.SessionName == "" {
		return Tokens{}, fmt.Errorf("%w: sid claim missing", errBadIDToken)
	}
	return o.tokensOf(exchanged, identity, ""), nil
}

func (o *OIDC) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	party, err := o.party(ctx)
	if err != nil {
		return Tokens{}, err
	}
	refreshed, err := rp.RefreshTokens[*oidc.IDTokenClaims](ctx, party, refreshToken, "", "")
	if err != nil {
		return Tokens{}, refreshError(err)
	}
	identity := Identity{}
	if refreshed.IDTokenClaims != nil {
		identity = identityOf(refreshed.IDTokenClaims)
	}
	return o.tokensOf(refreshed, identity, refreshToken), nil
}

func (o *OIDC) Revoke(ctx context.Context, refreshToken string) error {
	party, err := o.party(ctx)
	if err != nil {
		return err
	}
	return rp.RevokeToken(ctx, party, refreshToken, "refresh_token")
}

func (o *OIDC) tokensOf(tokens *oidc.Tokens[*oidc.IDTokenClaims], identity Identity, previousRefresh string) Tokens {
	refresh := tokens.RefreshToken
	if refresh == "" {
		refresh = previousRefresh
	}
	return Tokens{Access: tokens.AccessToken, Refresh: refresh, AccessExpiry: o.accessExpiry(tokens.Token), Identity: identity}
}

func (o *OIDC) accessExpiry(token *oauth2.Token) time.Time {
	if token.ExpiresIn > 0 {
		return o.cfg.Clock().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return token.Expiry
}

func (o *OIDC) party(ctx context.Context) (rp.RelyingParty, error) {
	host, err := o.cfg.Host(ctx)
	if err != nil {
		return nil, err
	}
	clientSecret, err := o.cfg.ClientSecret(ctx)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	key := partyKey{host: host, secret: clientSecret}
	if party, found := o.parties[key]; found {
		return party, nil
	}
	party, err := rp.NewRelyingPartyOIDC(ctx, "https://sso."+host, ConsoleClientID, clientSecret,
		"https://console."+host+PathCallback, scopeList(),
		rp.WithHTTPClient(o.client), rp.WithAuthStyle(oauth2.AuthStyleInHeader), rp.WithVerifierOpts(skipNonceCheck))
	if err != nil {
		return nil, fmt.Errorf("discover the issuer: %w", err)
	}
	o.parties[key] = party
	return party, nil
}

func skipNonceCheck(v *rp.IDTokenVerifier) { v.Nonce = nil }

func scopeList() []string {
	return []string{"openid", "profile", "email", "groups", "offline_access"}
}

func withNonceParam(nonce string) rp.AuthURLOpt {
	return func() []oauth2.AuthCodeOption {
		return []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("nonce", nonce)}
	}
}

func identityOf(claims *oidc.IDTokenClaims) Identity {
	name, _ := claims.Claims["name"].(string)
	email, _ := claims.Claims["email"].(string)
	session, _ := claims.Claims["sid"].(string)
	return Identity{Subject: claims.Subject, Name: name, Email: email, Groups: stringList(claims.Claims["groups"]), SessionName: session}
}

func stringList(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func refreshError(err error) error {
	var failure *oidc.Error
	if errors.As(err, &failure) && failure.ErrorType == oidc.InvalidGrant {
		return fmt.Errorf("refresh token grant: %w", ErrInvalidGrant)
	}
	return fmt.Errorf("refresh token grant: %w", err)
}

func authnHTTPClient(cfg OIDCConfig) *http.Client {
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(cfg.AuthnCA)
	dialer := &net.Dialer{Timeout: oidcTimeout}
	return &http.Client{
		Timeout: oidcTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: cfg.AuthnServerName},
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, err := cfg.Host(ctx)
				if err != nil {
					return nil, err
				}
				if address != net.JoinHostPort("sso."+host, "443") {
					return nil, fmt.Errorf("refusing to dial %s", address)
				}
				return dialer.DialContext(ctx, network, cfg.AuthnAddress)
			},
		},
	}
}
