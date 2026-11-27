package saxess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestMobileApprovalAcrossBackendInstances(t *testing.T) {
	testMobileApproval(t, fake.NewClientBuilder().Build())
}

func TestMobileApprovalWithAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	testMobileApproval(t, c)
}

func testMobileApproval(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "saxess-state"},
		Data:       map[string][]byte{stateKey: []byte("{}")},
	}
	if err := c.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	newBackend := func() *Backend {
		return NewBackend(NewSecretStore(c, c, client.ObjectKeyFromObject(secret)), VerifyCardProof)
	}
	browser, mobile := newBackend(), newBackend()
	const operator = "operator-uid"
	const email = "operator@example.com"
	const address = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"
	key := secp256k1.PrivKeyFromBytes([]byte{1})
	sign := func(payload string) string {
		digest := sha256.Sum256([]byte(payload))
		compact := ecdsa.SignCompact(key, digest[:], false)
		return "0x" + hex.EncodeToString(append(compact[1:], compact[0]-27))
	}
	invite, err := browser.CreateInvite(ctx, operator, email, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{
		"invite_token": invite.Payload, "public_key": address, "signed_msg": sign(invite.Payload),
		"device_type": "android", "fingerprint": "test-device",
	})
	register := func() *httptest.ResponseRecorder {
		return serveMobile(NewRegistrationHTTP(mobile).RegisterDevice, http.MethodPost,
			"/api/employees/devices/register", "Bearer "+invite.Token, "application/json", string(payload))
	}
	res := register()
	var enrolled RegisteredDevice
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &enrolled) != nil ||
		enrolled.Status != "pending" || enrolled.ID <= 0 || enrolled.UserID <= 0 {
		t.Fatalf("registration status %d: %s", res.Code, res.Body)
	}
	token := res.Header().Get("X-SAxess-Device-Token")
	if token == "" {
		t.Fatal("registration returned no mobile credential")
	}
	if got := register().Code; got == http.StatusOK {
		t.Fatal("registration accepted consumed invite")
	}
	device := strconv.FormatInt(enrolled.ID, 10)
	if _, err := mobile.Authenticate(ctx, token); !errors.Is(err, ErrMobileUnauthorized) {
		t.Fatalf("pending device authenticated: %v", err)
	}
	if err := browser.ApproveDevice(ctx, operator, device, address); err != nil {
		t.Fatal(err)
	}
	challenge, err := browser.CreateChallenge(ctx, operator, "browser-login", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := browser.CreateChallenge(ctx, operator, "browser-login", time.Minute); err != nil || again.Nonce != challenge.Nonce ||
		!again.ExpiresAt.Equal(challenge.ExpiresAt) {
		t.Fatalf("browser transaction retry changed the challenge: %v", err)
	}
	h := NewMobileHTTP(mobile.Authenticate, mobile)
	list := serveMobile(h.GetRequests, http.MethodPost, "/api/employees/get-requests", "Bearer "+token, "", "")
	var requests []MobileRequest
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &requests) != nil || len(requests) != 1 {
		t.Fatalf("list status %d: %s", list.Code, list.Body)
	}
	row := requests[0]
	if row.AuthReqID != challenge.Nonce || row.Status != "pending" || row.Notify.Data.SignPayload != challenge.Nonce ||
		row.Notify.Data.Type != "DASHBOARD_LOGIN_AUTH_REQUEST" {
		t.Fatalf("unexpected request: %+v", row)
	}
	approve := func() int {
		body, _ := json.Marshal(map[string]string{
			"auth_req_id": row.AuthReqID, "email": email, "device_id": device, "signed_msg": sign(row.Notify.Data.SignPayload),
		})
		return serveMobile(h.SendConsent, http.MethodPost, "/api/auth/send_consent", "Bearer "+token, "application/json", string(body)).Code
	}
	if got := approve(); got != http.StatusOK {
		t.Fatalf("approval status = %d", got)
	}
	if got := approve(); got != http.StatusForbidden {
		t.Fatalf("replayed approval status = %d", got)
	}
	if _, err := browser.ConsumeChallenge(ctx, operator, "other-browser", challenge.Nonce); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("wrong browser: %v", err)
	}
	behind := newBackend()
	behind.now = func() time.Time { return challenge.ExpiresAt.Add(-2 * time.Minute) }
	if _, err := behind.ConsumeChallenge(ctx, operator, "browser-login", challenge.Nonce); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("clock rollback accepted: %v", err)
	}
	results := make(chan error, 2)
	for _, backend := range []*Backend{browser, newBackend()} {
		go func() {
			id, err := backend.ConsumeChallenge(ctx, operator, "browser-login", challenge.Nonce)
			if err == nil && id != operator {
				err = errors.New("wrong operator")
			}
			results <- err
		}()
	}
	a, b := <-results, <-results
	if !(a == nil && errors.Is(b, ErrInvalidChallenge) || b == nil && errors.Is(a, ErrInvalidChallenge)) {
		t.Fatalf("want one successful consume; got %v, %v", a, b)
	}
	if got, err := browser.RedeemBrowserApproval(ctx, operator, "browser-login", challenge.Nonce); err != nil || got != operator {
		t.Fatalf("browser retry did not recover the approved transaction: %q, %v", got, err)
	}
	if _, err := browser.RedeemBrowserApproval(ctx, operator, "other-browser", challenge.Nonce); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("browser retry accepted a different transaction: %v", err)
	}
}
