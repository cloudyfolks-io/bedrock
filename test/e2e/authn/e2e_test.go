//go:build e2e

package authn

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/account"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	clientCLI        = "bedrock-cli"
	clientE2EOIDC    = "e2e-oidc"
	clientExchange   = "e2e-exchange"
	exchangeSecret   = "e2e-exchange-client-secret"
	exchangeAudience = "dadehat-test"
	svcTokenUser     = "svc-token-user"
	lockoutUser      = "lockout-target"
	lockoutPassword  = "lockout-target-pw"
	disableDeadline  = 60 * time.Second
	devicePrompt     = time.Minute
	deviceApproval   = 2 * time.Minute
)

var (
	loginScopes   = []string{"openid", "profile", "email", "groups"}
	offlineScopes = []string{"openid", "profile", "email", "groups", "offline_access"}
)

type env struct {
	host          string
	issuer        string
	roots         *x509.CertPool
	caFile        string
	api           apiServer
	k8s           client.Client
	adminPassword string
	bin           string
	home          string
	path          string
	budget        *attemptBudget
}

type adminLogin struct {
	totp          totpState
	recoveryCodes []string
}

type clusterInfo struct {
	Server               string `json:"server"`
	CertificateAuthority string `json:"certificateAuthority"`
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set", name)
	}
	return v
}

func absolutePath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(requireEnv(t, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return path
}

func kubeClient(t *testing.T) client.Client {
	t.Helper()
	restConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

func setupEnv(t *testing.T) env {
	t.Helper()
	host := requireEnv(t, "BEDROCK_HOST")
	caFile := absolutePath(t, "BEDROCK_CA")
	ca, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("read BEDROCK_CA: %v", err)
	}
	bin := absolutePath(t, "BEDROCK_BIN")
	e := env{
		host:          host,
		issuer:        "https://sso." + host,
		roots:         certPool(t, ca),
		caFile:        caFile,
		k8s:           kubeClient(t),
		adminPassword: requireEnv(t, "ADMIN_PASSWORD"),
		bin:           bin,
		home:          t.TempDir(),
		path:          bedrockOnPath(t, bin),
		budget:        &attemptBudget{},
	}
	e.api = clusterAPI(t, e)
	return e
}

func clusterAPI(t *testing.T, e env) apiServer {
	t.Helper()
	c := newLoginClient(t, e)
	status, body := c.roundTrip(t, jsonRequest(t, http.MethodGet, e.issuer+"/api/v1/cluster", "", nil))
	if status != http.StatusOK {
		t.Fatalf("cluster info: status %d: %s", status, body)
	}
	var info clusterInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("cluster info: %v: %s", err, body)
	}
	if info.Server != "https://api."+e.host+":6443" || info.CertificateAuthority == "" {
		t.Fatalf("cluster info: server %q with a %d byte CA, want https://api.%s:6443 with a CA", info.Server, len(info.CertificateAuthority), e.host)
	}
	caFile := filepath.Join(t.TempDir(), "kube-ca.crt")
	if err := os.WriteFile(caFile, []byte(info.CertificateAuthority), 0o600); err != nil {
		t.Fatalf("write cluster CA: %v", err)
	}
	return apiServer{server: info.Server, caFile: caFile}
}

func decodeSeed(t *testing.T, encoded string) []byte {
	t.Helper()
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(encoded))
	if err != nil {
		t.Fatalf("decode totp secret: %v", err)
	}
	return seed
}

func requireChallenge(t *testing.T, who string, ch challenge, want string) {
	t.Helper()
	if ch.Type != want || ch.Error != nil {
		t.Fatalf("%s: challenge is %q with error %v, want %s", who, ch.Type, ch.Error, want)
	}
}

func requireFailure(t *testing.T, who string, ch challenge, want string) {
	t.Helper()
	if ch.Error == nil || ch.Error.Code != want {
		t.Fatalf("%s: challenge %q has error %v, want %s", who, ch.Type, ch.Error, want)
	}
}

func hasProvider(providers []providerChoice, name string) bool {
	return slices.ContainsFunc(providers, func(p providerChoice) bool { return p.Name == name })
}

func TestAuthnEndToEnd(t *testing.T) {
	e := setupEnv(t)

	var admin adminLogin
	t.Run("admin password login enrolls totp and kube-apiserver accepts it", func(t *testing.T) {
		admin = testAdminPasswordAndTOTP(t, e)
	})

	var svcToken string
	t.Run("an API token authenticates through kubectl", func(t *testing.T) {
		svcToken = testAPIToken(t, e)
	})

	t.Run("an LDAP user is allowed in team-a and denied in default", func(t *testing.T) {
		testLDAPUser(t, e)
	})

	t.Run("an OIDC user through Dex maps groups and needs bedrock totp", func(t *testing.T) {
		testDexUser(t, e)
	})

	t.Run("device flow login writes a kubeconfig with an exec credential", func(t *testing.T) {
		requireAdmin(t, admin)
		admin.totp = testDeviceFlowAndKubeconfig(t, e, admin.totp)
	})

	t.Run("refresh rotation revokes the family on reuse", func(t *testing.T) {
		requireAdmin(t, admin)
		admin.totp = testRefreshRotation(t, e, admin.totp)
	})

	t.Run("token exchange allows a listed audience and refuses others", func(t *testing.T) {
		requireAdmin(t, admin)
		testTokenExchange(t, e, admin.recoveryCodes[0])
	})

	t.Run("password lockout after the threshold", func(t *testing.T) {
		testLockout(t, e)
	})

	t.Run("a disabled user loses its API token", func(t *testing.T) {
		if svcToken == "" {
			t.Skip("needs the API token of the earlier subtest")
		}
		testDisabledUser(t, e, svcToken)
	})
}

func requireAdmin(t *testing.T, admin adminLogin) {
	t.Helper()
	if admin.totp.seed == nil || len(admin.recoveryCodes) == 0 {
		t.Skip("needs the admin TOTP enrollment of the first subtest")
	}
}

func testAdminPasswordAndTOTP(t *testing.T, e env) adminLogin {
	t.Helper()
	c := newLoginClient(t, e)
	verifier := randomToken(32)
	ch := c.startAuthorize(t, clientE2EOIDC, verifier, loginScopes)
	requireChallenge(t, "admin: start", ch, "username")
	requireCrossSiteRefused(t, c, ch)
	ch = c.username(t, ch, v1alpha1.UserAdmin)
	requireChallenge(t, "admin: after username", ch, "password")
	ch = c.password(t, ch, e.adminPassword)
	requireChallenge(t, "admin: after password", ch, "totp-enroll")
	if ch.Enroll == nil || ch.Enroll.Secret == "" {
		t.Fatalf("admin: the totp-enroll challenge carries no secret")
	}
	ch, state := c.totpAnswer(t, ch, "totp-enroll", totpState{seed: decodeSeed(t, ch.Enroll.Secret)})
	requireChallenge(t, "admin: after the enrollment code", ch, "totp-enroll")
	if len(ch.RecoveryCodes) != 10 {
		t.Fatalf("admin: enrollment gave %d recovery codes, want 10", len(ch.RecoveryCodes))
	}
	recoveryCodes := ch.RecoveryCodes
	ch = c.acknowledgeRecoveryCodes(t, ch)
	requireChallenge(t, "admin: after acknowledging the recovery codes", ch, "done")
	requireSessionBound(t, e, ch.Redirect)
	requireClientAddress(t, e, v1alpha1.UserObjectName(v1alpha1.UserAdmin))
	tokens := tokensFrom(t, e, tokenRequest(t, e, codeForm(clientE2EOIDC, verifier, c.finishLogin(t, ch))))
	amr := claimStrings(jwtPayload(t, tokens.AccessToken)["amr"])
	if !slices.Contains(amr, "pwd") || !slices.Contains(amr, "otp") {
		t.Fatalf("admin: access token amr = %v, want pwd and otp", amr)
	}
	whoami := kubectlWhoami(t, e.api, tokens.AccessToken)
	if whoami.Status.UserInfo.Username != "bedrock:admin" {
		t.Fatalf("admin: whoami username = %q, want bedrock:admin", whoami.Status.UserInfo.Username)
	}
	if !slices.Contains(whoami.Status.UserInfo.Groups, "bedrock:bedrock-admins") {
		t.Fatalf("admin: whoami groups = %v, want bedrock:bedrock-admins", whoami.Status.UserInfo.Groups)
	}
	return adminLogin{totp: state, recoveryCodes: recoveryCodes}
}

func requireCrossSiteRefused(t *testing.T, c *loginClient, ch challenge) {
	t.Helper()
	req := jsonRequest(t, http.MethodPost, c.base+"/api/v1/login/answer", ch.CSRF, loginAnswer{Type: "username", Username: v1alpha1.UserAdmin})
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	status, body := c.roundTrip(t, req)
	if status != http.StatusForbidden || !strings.Contains(string(body), `"csrf"`) {
		t.Fatalf("cross-site answer: status %d: %s, want 403 csrf", status, body)
	}
}

func requireSessionBound(t *testing.T, e env, redirect string) {
	t.Helper()
	stranger := newLoginClient(t, e)
	resp := stranger.get(t, redirect)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("authorize callback without the session cookie: status %d, want 401", resp.StatusCode)
	}
}

func latestSession(sessions []v1alpha1.Session, userRef string) (v1alpha1.Session, bool) {
	var latest v1alpha1.Session
	found := false
	for _, session := range sessions {
		if session.Spec.UserRef != userRef {
			continue
		}
		if !found || session.Spec.AuthTime.After(latest.Spec.AuthTime.Time) {
			latest, found = session, true
		}
	}
	return latest, found
}

func parseClientAddress(recorded string) (net.IP, error) {
	address := net.ParseIP(recorded)
	if address == nil {
		return nil, fmt.Errorf("session client IP %q is not an IP address", recorded)
	}
	return address, nil
}

func requireClientAddress(t *testing.T, e env, userRef string) {
	t.Helper()
	var sessions v1alpha1.SessionList
	if err := e.k8s.List(context.Background(), &sessions, client.InNamespace(release.SystemNamespace)); err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	session, found := latestSession(sessions.Items, userRef)
	if !found {
		t.Fatalf("no session for user %s after the login", userRef)
	}
	address, err := parseClientAddress(session.Spec.ClientIP)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the newest session of %s records client IP %s", userRef, address)
}

func testAPIToken(t *testing.T, e env) string {
	t.Helper()
	ctx := context.Background()
	user := newUser(svcTokenUser, nil)
	if err := e.k8s.Create(ctx, &user); err != nil {
		t.Fatalf("create user %s: %v", svcTokenUser, err)
	}
	token, err := secret.NewAPIToken(rand.Reader)
	if err != nil {
		t.Fatalf("new api token: %v", err)
	}
	apiToken := account.NewToken(user.Name, "e2e-authn", nil, nil, token)
	if err := e.k8s.Create(ctx, &apiToken); err != nil {
		t.Fatalf("create api token: %v", err)
	}
	whoami := kubectlWhoami(t, e.api, token)
	if whoami.Status.UserInfo.Username != "bedrock:"+svcTokenUser {
		t.Fatalf("api token: whoami username = %q, want bedrock:%s", whoami.Status.UserInfo.Username, svcTokenUser)
	}
	return token
}

func newUser(username string, loginMethods []string) v1alpha1.User {
	name := v1alpha1.UserObjectName(username)
	return v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: release.SystemNamespace,
			Labels:    map[string]string{v1alpha1.LabelKind: "User", v1alpha1.LabelName: name},
		},
		Spec: v1alpha1.UserSpec{Username: username, DisplayName: username, Methods: loginMethods},
	}
}

func createLocalUser(t *testing.T, e env, username, password string) v1alpha1.User {
	t.Helper()
	ctx := context.Background()
	user := newUser(username, []string{v1alpha1.MethodPassword})
	if err := e.k8s.Create(ctx, &user); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	if err := methods.SetPassword(ctx, e.k8s, rand.Reader, user, password); err != nil {
		t.Fatalf("set password for %s: %v", username, err)
	}
	return user
}

func testLDAPUser(t *testing.T, e env) {
	t.Helper()
	c := newLoginClient(t, e)
	verifier := randomToken(32)
	ch := c.startAuthorize(t, clientCLI, verifier, loginScopes)
	ch = c.username(t, ch, "alice")
	requireChallenge(t, "alice: after username", ch, "password")
	ch = c.password(t, ch, "alice-e2e-pw")
	requireChallenge(t, "alice: after password", ch, "done")
	tokens := tokensFrom(t, e, tokenRequest(t, e, codeForm(clientCLI, verifier, c.finishLogin(t, ch))))
	whoami := kubectlWhoami(t, e.api, tokens.AccessToken)
	if whoami.Status.UserInfo.Username != "bedrock:alice" || !slices.Contains(whoami.Status.UserInfo.Groups, "bedrock:ops") {
		t.Fatalf("alice: whoami = %q in %v, want bedrock:alice in bedrock:ops", whoami.Status.UserInfo.Username, whoami.Status.UserInfo.Groups)
	}
	if !kubectlCanI(t, e.api, tokens.AccessToken, "get", "pods", "team-a") {
		t.Fatalf("alice: denied get pods in team-a, want allowed")
	}
	if kubectlCanI(t, e.api, tokens.AccessToken, "get", "pods", "default") {
		t.Fatalf("alice: allowed get pods in default, want denied")
	}
}

func testDexUser(t *testing.T, e env) {
	t.Helper()
	c := newLoginClient(t, e)
	verifier := randomToken(32)
	ch := c.startAuthorize(t, clientE2EOIDC, verifier, loginScopes)
	if ch.Type != "username" || !hasProvider(ch.Providers, "dex") {
		t.Fatalf("carol: the first challenge is %q with providers %v, want username listing dex", ch.Type, ch.Providers)
	}
	ch = c.provider(t, ch, "dex")
	requireChallenge(t, "carol: after choosing dex", ch, "redirect")
	issuerHost := hostOf(t, e.issuer)
	loginPage := followUntil(t, c, c.get(t, ch.Redirect), passwordForm(issuerHost))
	followUntil(t, c, c.postUpstreamLogin(t, loginPage, "carol@example.test", "dex-carol-test-pw"), loginApp(issuerHost))
	ch = c.current(t)
	requireChallenge(t, "carol: after the dex callback", ch, "totp-enroll")
	if ch.Enroll == nil || ch.Enroll.Secret == "" {
		t.Fatalf("carol: the totp-enroll challenge carries no secret")
	}
	ch, _ = c.totpAnswer(t, ch, "totp-enroll", totpState{seed: decodeSeed(t, ch.Enroll.Secret)})
	requireChallenge(t, "carol: after the enrollment code", ch, "totp-enroll")
	ch = c.acknowledgeRecoveryCodes(t, ch)
	requireChallenge(t, "carol: after acknowledging the recovery codes", ch, "done")
	tokens := tokensFrom(t, e, tokenRequest(t, e, codeForm(clientE2EOIDC, verifier, c.finishLogin(t, ch))))
	claims := jwtPayload(t, tokens.AccessToken)
	if groups := claimStrings(claims["groups"]); !slices.Contains(groups, "ops") {
		t.Fatalf("carol: access token groups = %v, want ops", groups)
	}
	if amr := claimStrings(claims["amr"]); !slices.Contains(amr, "fed") || !slices.Contains(amr, "otp") {
		t.Fatalf("carol: access token amr = %v, want fed and otp", amr)
	}
	whoami := kubectlWhoami(t, e.api, tokens.AccessToken)
	if !slices.Contains(whoami.Status.UserInfo.Groups, "bedrock:ops") {
		t.Fatalf("carol: whoami groups = %v, want bedrock:ops", whoami.Status.UserInfo.Groups)
	}
	if !kubectlCanI(t, e.api, tokens.AccessToken, "get", "pods", "team-a") {
		t.Fatalf("carol: denied get pods in team-a, want allowed")
	}
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %s: %v", rawURL, err)
	}
	return parsed.Host
}

func testDeviceFlowAndKubeconfig(t *testing.T, e env, state totpState) totpState {
	t.Helper()
	proc := startBedrockLogin(t, e)
	userCode := proc.userCode(t, devicePrompt)
	c := newLoginClient(t, e)
	ch := c.startDevice(t, userCode)
	requireChallenge(t, "device flow: after entering the code", ch, "username")
	ch = c.username(t, ch, v1alpha1.UserAdmin)
	ch = c.password(t, ch, e.adminPassword)
	requireChallenge(t, "device flow: after password", ch, "totp")
	ch, next := c.totpAnswer(t, ch, "totp", state)
	requireChallenge(t, "device flow: after totp", ch, "device-confirm")
	if ch.Device == nil || ch.Device.ClientID != clientCLI {
		t.Fatalf("device flow: the confirmation names %+v, want client %s", ch.Device, clientCLI)
	}
	ch = c.approveDevice(t, ch)
	requireChallenge(t, "device flow: after approving", ch, "done")
	if result := proc.wait(t, deviceApproval); !strings.Contains(result.stdout, "logged in as admin") {
		t.Fatalf("device flow: bedrock login printed %q, want logged in as admin", result.stdout)
	}

	skipped := runBedrock(e, "login", "--server", e.issuer, "--ca-file", e.caFile, "--write-kubeconfig=false")
	if skipped.err != nil || !strings.Contains(skipped.stdout, "logged in as admin") {
		t.Fatalf("bedrock login --write-kubeconfig=false: %v: %s%s", skipped.err, skipped.stdout, skipped.stderr)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".kube", "config")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bedrock login --write-kubeconfig=false wrote a kubeconfig: %v", err)
	}

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	written := runBedrock(e, "login", "--server", e.issuer, "--ca-file", e.caFile, "--write-kubeconfig="+kubeconfig)
	if written.err != nil {
		t.Fatalf("bedrock login --write-kubeconfig: %v: %s", written.err, written.stderr)
	}
	whoami := decodeWhoami(t, kubectlWithKubeconfig(t, e, kubeconfig, "auth", "whoami", "-o", "json"))
	if whoami.Status.UserInfo.Username != "bedrock:admin" {
		t.Fatalf("device flow kubeconfig: whoami username = %q, want bedrock:admin", whoami.Status.UserInfo.Username)
	}
	return next
}

func testRefreshRotation(t *testing.T, e env, state totpState) totpState {
	t.Helper()
	c := newLoginClient(t, e)
	verifier := randomToken(32)
	ch := c.startAuthorize(t, clientCLI, verifier, offlineScopes)
	ch = c.username(t, ch, v1alpha1.UserAdmin)
	ch = c.password(t, ch, e.adminPassword)
	requireChallenge(t, "refresh rotation: after password", ch, "totp")
	ch, next := c.totpAnswer(t, ch, "totp", state)
	first := tokensFrom(t, e, tokenRequest(t, e, codeForm(clientCLI, verifier, c.finishLogin(t, ch))))
	if first.RefreshToken == "" {
		t.Fatalf("refresh rotation: no refresh token issued")
	}
	second := tokensFrom(t, e, tokenRequest(t, e, refreshForm(clientCLI, first.RefreshToken)))
	if second.RefreshToken == "" || second.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh rotation: rotation did not issue a new refresh token")
	}
	tokenRefusal(t, e, tokenRequest(t, e, refreshForm(clientCLI, first.RefreshToken)))
	tokenRefusal(t, e, tokenRequest(t, e, refreshForm(clientCLI, second.RefreshToken)))
	return next
}

func testTokenExchange(t *testing.T, e env, recoveryCode string) {
	t.Helper()
	c := newLoginClient(t, e)
	verifier := randomToken(32)
	ch := c.startAuthorize(t, clientCLI, verifier, loginScopes)
	ch = c.username(t, ch, v1alpha1.UserAdmin)
	ch = c.password(t, ch, e.adminPassword)
	requireChallenge(t, "token exchange: after password", ch, "totp")
	if !slices.Contains(ch.Methods, v1alpha1.MethodRecovery) {
		t.Fatalf("token exchange: the second factor step offers %v, want recovery", ch.Methods)
	}
	ch = c.recovery(t, ch, recoveryCode)
	requireChallenge(t, "token exchange: after a recovery code", ch, "done")
	subject := tokensFrom(t, e, tokenRequest(t, e, codeForm(clientCLI, verifier, c.finishLogin(t, ch))))

	exchanged := tokensFrom(t, e, clientTokenRequest(t, e, exchangeForm(subject.AccessToken, exchangeAudience), clientExchange, exchangeSecret))
	claims := jwtPayload(t, exchanged.AccessToken)
	if aud := claimStrings(claims["aud"]); !slices.Contains(aud, exchangeAudience) {
		t.Fatalf("token exchange: aud = %v, want %s", aud, exchangeAudience)
	}
	if claims["sub"] != v1alpha1.UserAdmin {
		t.Fatalf("token exchange: sub = %v, want %s", claims["sub"], v1alpha1.UserAdmin)
	}
	act, _ := claims["act"].(map[string]any)
	if act["sub"] != clientExchange {
		t.Fatalf("token exchange: act.sub = %v, want %s", act["sub"], clientExchange)
	}

	refused := tokenRefusal(t, e, clientTokenRequest(t, e, exchangeForm(subject.AccessToken, "not-allowed"), clientExchange, exchangeSecret))
	if refused.Error != "invalid_target" {
		t.Fatalf("token exchange: refusal error = %q, want invalid_target", refused.Error)
	}
}

func testLockout(t *testing.T, e env) {
	t.Helper()
	ctx := context.Background()
	settings, err := policy.ReadSettings(ctx, e.k8s)
	if err != nil {
		t.Fatalf("read authn settings: %v", err)
	}
	user := createLocalUser(t, e, lockoutUser, lockoutPassword)
	c := newLoginClient(t, e)
	ch := c.startAuthorize(t, clientCLI, randomToken(32), []string{"openid"})
	ch = c.username(t, ch, lockoutUser)
	requireChallenge(t, "lockout: after username", ch, "password")
	for attempt := 1; attempt <= settings.LockoutThreshold; attempt++ {
		ch = c.password(t, ch, "wrong-password")
		requireFailure(t, "lockout: wrong password", ch, methods.FailureInvalidCredentials)
	}
	ch = c.password(t, ch, lockoutPassword)
	requireFailure(t, "lockout: the right password while locked", ch, methods.FailureInvalidCredentials)
	var locked v1alpha1.User
	if err := e.k8s.Get(ctx, client.ObjectKeyFromObject(&user), &locked); err != nil {
		t.Fatalf("get user %s: %v", user.Name, err)
	}
	if !methods.Locked(locked.Status, time.Now()) || locked.Status.Locks < 1 {
		t.Fatalf("lockout: user status %+v, want a lock in effect", locked.Status)
	}
}

func disableUser(t *testing.T, e env, name string) {
	t.Helper()
	ctx := context.Background()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var user v1alpha1.User
		if err := e.k8s.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &user); err != nil {
			return err
		}
		user.Spec.Disabled = true
		return e.k8s.Update(ctx, &user)
	})
	if err != nil {
		t.Fatalf("disable user %s: %v", name, err)
	}
}

func testDisabledUser(t *testing.T, e env, svcToken string) {
	t.Helper()
	if refused, stderr := kubectlRefused(e.api, svcToken); refused {
		t.Fatalf("disabled user: the api token must still work before the user is disabled: %s", stderr)
	}
	disableUser(t, e, v1alpha1.UserObjectName(svcTokenUser))
	deadline := time.Now().Add(disableDeadline)
	for {
		refused, stderr := kubectlRefused(e.api, svcToken)
		if refused {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("disabled user: the api token still authenticates %s after being disabled: %s", disableDeadline, stderr)
		}
		time.Sleep(3 * time.Second)
	}
}
