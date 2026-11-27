package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/saxess"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSAxessLoginUsesOnlyTheConsoleClient(t *testing.T) {
	b := &saxess.Backend{}
	cfg, err := saxessLoginConfig(context.Background(), b, func(context.Context) (string, error) { return "lab.example", nil })
	if err != nil || cfg == nil || cfg.Backend != b || cfg.ClientID != "bedrock-console" ||
		cfg.RedirectURI != "https://console.lab.example/oauth/callback" {
		t.Fatalf("unexpected config: %+v, %v", cfg, err)
	}
}

func TestSAxessMobileEndpointsAreOptIn(t *testing.T) {
	for _, path := range []string{"/api/employees/devices/register", "/api/employees/get-details", "/api/employees/get-requests", "/api/auth/send_consent"} {
		for _, tc := range []struct {
			backend *saxess.Backend
			status  int
		}{
			{nil, http.StatusNotFound},
			{&saxess.Backend{}, http.StatusUnauthorized},
		} {
			mux := http.NewServeMux()
			mountSAxessMobile(mux, tc.backend, time.Now)
			res := httptest.NewRecorder()
			mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, nil))
			if res.Code != tc.status {
				t.Fatalf("%s: got %d, want %d", path, res.Code, tc.status)
			}
		}
	}
}

func TestSAxessConsoleLoginThroughMobileAPI(t *testing.T) {
	ctx := context.Background()
	c, cfg := startTestEnv(t)
	seedCluster(t, c)
	saveSigningKey(t, c, time.Now().Add(-time.Minute))
	redirect := "https://console." + testHost + "/oauth/callback"
	namespace := release.SystemNamespace
	state := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "saxess-state"},
		Data:       map[string][]byte{"state.json": []byte("{}")},
	}
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: v1alpha1.UserObjectName("admin")},
		Spec: v1alpha1.UserSpec{Username: "admin", Email: "operator@example.com", Groups: []string{v1alpha1.GroupAdmins},
			Methods: []string{v1alpha1.MethodPassword}},
	}
	oauth := &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "bedrock-console"},
		Spec: v1alpha1.OAuthClientSpec{
			ClientID: "bedrock-console", Public: true, RedirectURIs: []string{redirect},
			GrantTypes: []string{v1alpha1.GrantAuthorizationCode},
		},
	}
	for _, object := range []client.Object{state, user, oauth} {
		if err := c.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	backend := saxess.NewBackend(saxess.NewSecretStore(c, c, client.ObjectKeyFromObject(state)), func(key, payload, proof string) error {
		if key != "test-card" || proof != "proof:"+payload {
			return errors.New("bad proof")
		}
		return nil
	})
	handler, err := New(ctx, Config{RestConfig: cfg, Random: rand.Reader, Clock: time.Now, SAxess: backend})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := noRedirects(server, jar)
	host := "sso." + testHost
	mobile := func(path, token string, body map[string]string) *http.Response {
		t.Helper()
		header := http.Header{"Authorization": {"Bearer " + token}}
		if body == nil {
			return request(t, server.Client(), server, http.MethodPost, host, path, nil, header)
		}
		header.Set("Content-Type", "application/json")
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return request(t, server.Client(), server, http.MethodPost, host, path, strings.NewReader(string(data)), header)
	}
	invite, err := backend.CreateBedrockInvite(ctx, c, client.ObjectKeyFromObject(user), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	registration := mobile("/api/employees/devices/register", invite.Token, map[string]string{
		"invite_token": invite.Payload, "device_type": "ios", "fingerprint": "test-phone",
		"public_key": "test-card", "signed_msg": "proof:" + invite.Payload,
	})
	device := readJSON[saxess.RegisteredDevice](t, registration, http.StatusOK)
	token := registration.Header.Get("X-SAxess-Device-Token")
	deviceID := strconv.FormatInt(device.ID, 10)
	if err := backend.ApproveDevice(ctx, string(user.UID), deviceID, "test-card"); err != nil {
		t.Fatal(err)
	}
	details := readJSON[struct {
		Email string `json:"email"`
	}](t, mobile("/api/employees/get-details", token, nil), http.StatusOK)
	if details.Email != user.Spec.Email {
		t.Fatalf("mobile details did not resolve to the enrolled operator: %q", details.Email)
	}
	verifier := strings.Repeat("v", 48)
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id": {"bedrock-console"}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {"openid"}, "state": {"console-state"}, "code_challenge_method": {"S256"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
	}
	auth := request(t, browser, server, http.MethodGet, host, pathAuthorize+"?"+query.Encode(), nil, nil)
	loginURL, err := url.Parse(auth.Header.Get("Location"))
	if err != nil || loginURL.Query().Get("authRequest") == "" {
		t.Fatalf("authorization status %d, location %q", auth.StatusCode, auth.Header.Get("Location"))
	}
	step, _ := postJSON(t, browser, server, pathLogin+"start", "", map[string]string{"authRequest": loginURL.Query().Get("authRequest")})
	if !step.SAxess {
		t.Fatal("console did not offer sAxess")
	}
	step, _ = postJSON(t, browser, server, pathLoginAnswer, step.CSRF, methods.Answer{Type: login.AnswerSAxessStart, Username: user.Spec.Username})
	if step.Type != login.ChallengeSAxess {
		t.Fatalf("unexpected step: %+v", step)
	}
	rows := readJSON[[]saxess.MobileRequest](t, mobile("/api/employees/get-requests", token, nil), http.StatusOK)
	if len(rows) != 1 || rows[0].Status != "pending" {
		t.Fatalf("unexpected requests: %+v", rows)
	}
	readJSON[map[string]string](t, mobile("/api/auth/send_consent", token, map[string]string{
		"auth_req_id": rows[0].AuthReqID, "device_id": deviceID, "email": user.Spec.Email,
		"signed_msg": "proof:" + rows[0].Notify.Data.SignPayload,
	}), http.StatusOK)
	step, _ = postJSON(t, browser, server, pathLoginAnswer, step.CSRF, methods.Answer{Type: login.ChallengeSAxess})
	callbackURL, err := url.Parse(step.Redirect)
	if err != nil || step.Type != methods.ChallengeDone || callbackURL.Query().Get("id") == "" {
		t.Fatalf("login did not complete: %+v", step)
	}
	callback := request(t, browser, server, http.MethodGet, host, callbackURL.RequestURI(), nil, nil)
	location, err := url.Parse(callback.Header.Get("Location"))
	if err != nil || callback.StatusCode != http.StatusFound || location.Query().Get("code") == "" ||
		location.Query().Get("state") != "console-state" {
		t.Fatalf("callback status %d, location %q", callback.StatusCode, callback.Header.Get("Location"))
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")}, "redirect_uri": {redirect},
		"client_id": {"bedrock-console"}, "code_verifier": {verifier},
	}
	tokens := readJSON[struct {
		IDToken string `json:"id_token"`
	}](t, request(t, browser, server, http.MethodPost, host, pathToken,
		strings.NewReader(form.Encode()), http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}), http.StatusOK)
	if claims := jwtClaims(t, tokens.IDToken); claims["sub"] != "admin" {
		t.Fatalf("unexpected Bedrock subject: %v", claims["sub"])
	}
}
