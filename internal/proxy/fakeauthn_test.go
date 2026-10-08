package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	fakeAuthnServerName = "authn.test"
	fakeIssuer          = "https://sso.lab.example"
	fakeClientSecret    = "s3cret"
)

type fakeIdentity struct {
	Subject, Name, Email, Session string
	Groups                        []string
}

type fakeFamily struct {
	current string
	revoked bool
}

type fakeCode struct {
	challenge, nonce string
}

type fakeAuthn struct {
	t       *testing.T
	server  *httptest.Server
	caPEM   []byte
	signing *ecdsa.PrivateKey
	other   *ecdsa.PrivateKey

	mu          sync.Mutex
	identity    fakeIdentity
	codes       map[string]fakeCode
	tokens      map[string]*fakeFamily
	calls       map[string]int
	counter     int
	expiresIn   int
	failStatus  int
	reuseErrors int
	lastBasic   string
	revoked     []string
	wrongNonce  bool
	wrongAud    bool
	wrongIssuer bool
	expired     bool
	noSID       bool
	badSig      bool
	noIDRefresh bool
}

func generateCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake authn ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func serverCertificate(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, dnsName string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func newFakeAuthn(t *testing.T) *fakeAuthn {
	t.Helper()
	ca, caKey, caPEM := generateCA(t)
	signing, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAuthn{
		t: t, caPEM: caPEM, signing: signing, other: other,
		identity:  fakeIdentity{Subject: "alice", Name: "Alice", Email: "alice@lab.example", Session: "sid-1", Groups: []string{"ops"}},
		codes:     map[string]fakeCode{},
		tokens:    map[string]*fakeFamily{},
		calls:     map[string]int{},
		expiresIn: 3600,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/oauth/v2/keys", f.keys)
	mux.HandleFunc("/oauth/v2/token", f.token)
	mux.HandleFunc("/oauth/v2/revoke", f.revoke)
	f.server = httptest.NewUnstartedServer(mux)
	f.server.TLS = &tls.Config{Certificates: []tls.Certificate{serverCertificate(t, ca, caKey, fakeAuthnServerName)}}
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAuthn) Address() string { return f.server.Listener.Addr().String() }

func (f *fakeAuthn) mutate(change func(*fakeAuthn)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeAuthn) Calls(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeAuthn) ReuseErrors() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reuseErrors
}

func (f *fakeAuthn) Revoked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.revoked...)
}

func (f *fakeAuthn) LastBasic() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastBasic
}

func (f *fakeAuthn) IssueCode(nonce, challenge string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counter++
	code := "code-" + strconv.Itoa(f.counter)
	f.codes[code] = fakeCode{challenge: challenge, nonce: nonce}
	return code
}

func (f *fakeAuthn) CodeFor(authorizeURL string) string {
	f.t.Helper()
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		f.t.Fatal(err)
	}
	query := parsed.Query()
	return f.IssueCode(query.Get("nonce"), query.Get("code_challenge"))
}

func (f *fakeAuthn) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
}

func (f *fakeAuthn) discovery(w http.ResponseWriter, r *http.Request) {
	f.record("discovery")
	issuer := "https://" + r.Host
	writeFakeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/v2/authorize",
		"token_endpoint":                        issuer + "/oauth/v2/token",
		"jwks_uri":                              issuer + "/oauth/v2/keys",
		"revocation_endpoint":                   issuer + "/oauth/v2/revoke",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"ES256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (f *fakeAuthn) keys(w http.ResponseWriter, _ *http.Request) {
	f.record("keys")
	key := jose.JSONWebKey{Key: &f.signing.PublicKey, KeyID: "k1", Algorithm: "ES256", Use: "sig"}
	writeFakeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{key}})
}

func (f *fakeAuthn) authenticated(w http.ResponseWriter, r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	f.mu.Lock()
	f.lastBasic = user
	f.mu.Unlock()
	if !ok || user != ConsoleClientID || pass != fakeClientSecret {
		writeFakeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return false
	}
	return true
}

func (f *fakeAuthn) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeFakeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	f.record("token:" + r.PostForm.Get("grant_type"))
	if !f.authenticated(w, r) {
		return
	}
	f.mu.Lock()
	failStatus := f.failStatus
	f.mu.Unlock()
	if failStatus != 0 {
		writeFakeJSON(w, failStatus, map[string]string{"error": "temporarily_unavailable"})
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		f.grantCode(w, r)
	case "refresh_token":
		f.grantRefresh(w, r)
	default:
		writeFakeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

func (f *fakeAuthn) grantCode(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	entry, found := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()
	if !found || pkceChallenge(r.PostForm.Get("code_verifier")) != entry.challenge || !strings.HasSuffix(r.PostForm.Get("redirect_uri"), PathCallback) {
		writeFakeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	f.mu.Lock()
	f.counter++
	family := &fakeFamily{current: "refresh-" + strconv.Itoa(f.counter)}
	f.tokens[family.current] = family
	access := "access-" + strconv.Itoa(f.counter)
	expiresIn := f.expiresIn
	f.mu.Unlock()
	f.respondTokens(w, access, family.current, entry.nonce, expiresIn, true)
}

func (f *fakeAuthn) grantRefresh(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	presented := r.PostForm.Get("refresh_token")
	family, found := f.tokens[presented]
	if !found || family.revoked || family.current != presented {
		if found {
			family.revoked = true
			f.reuseErrors++
		}
		f.mu.Unlock()
		writeFakeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	f.counter++
	family.current = "refresh-" + strconv.Itoa(f.counter)
	f.tokens[family.current] = family
	access := "access-" + strconv.Itoa(f.counter)
	expiresIn := f.expiresIn
	withID := !f.noIDRefresh
	f.mu.Unlock()
	f.respondTokens(w, access, family.current, "", expiresIn, withID)
}

func (f *fakeAuthn) respondTokens(w http.ResponseWriter, access, refresh, nonce string, expiresIn int, withID bool) {
	body := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": expiresIn, "refresh_token": refresh}
	if withID {
		idToken, err := f.idToken(nonce)
		if err != nil {
			writeFakeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}
		body["id_token"] = idToken
	}
	writeFakeJSON(w, http.StatusOK, body)
}

func (f *fakeAuthn) idToken(nonce string) (string, error) {
	f.mu.Lock()
	identity, wrongNonce, wrongAud, wrongIssuer, expired, noSID, badSig := f.identity, f.wrongNonce, f.wrongAud, f.wrongIssuer, f.expired, f.noSID, f.badSig
	f.mu.Unlock()
	now := time.Now()
	claims := map[string]any{
		"iss":    fakeIssuer,
		"sub":    identity.Subject,
		"aud":    []string{"bedrock", ConsoleClientID},
		"azp":    ConsoleClientID,
		"exp":    now.Add(time.Hour).Unix(),
		"iat":    now.Unix(),
		"groups": identity.Groups,
		"name":   identity.Name,
		"email":  identity.Email,
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	if !noSID {
		claims["sid"] = identity.Session
	}
	if wrongNonce {
		claims["nonce"] = "wrong-nonce"
	}
	if wrongAud {
		claims["aud"] = []string{"someone-else"}
	}
	if wrongIssuer {
		claims["iss"] = "https://evil.example"
	}
	if expired {
		claims["exp"] = now.Add(-time.Hour).Unix()
		claims["iat"] = now.Add(-2 * time.Hour).Unix()
	}
	key := f.signing
	if badSig {
		key = f.other
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}

func (f *fakeAuthn) revoke(w http.ResponseWriter, r *http.Request) {
	f.record("revoke")
	if err := r.ParseForm(); err != nil || !f.authenticated(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	token := r.PostForm.Get("token")
	f.revoked = append(f.revoked, token)
	if family, found := f.tokens[token]; found {
		family.revoked = true
	}
	w.WriteHeader(http.StatusOK)
}

func writeFakeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(fmt.Sprintf("encode fake answer: %v", err))
	}
}
