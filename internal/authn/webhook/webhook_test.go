package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const testBearer = "webhook-bearer-0123456789abcdefghij"

func TestReview(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	alice := v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice", UID: "uid-alice"}, Spec: v1alpha1.UserSpec{Username: "alice"}}
	disabled := *alice.DeepCopy()
	disabled.Spec.Disabled = true
	tokenFor := func(user string, expiresAt *metav1.Time) v1alpha1.APIToken {
		return v1alpha1.APIToken{Spec: v1alpha1.APITokenSpec{UserRef: user, ExpiresAt: expiresAt}}
	}
	future := metav1.NewTime(now.Add(time.Hour))
	exact := metav1.NewTime(now)
	past := metav1.NewTime(now.Add(-time.Second))
	valid := authenticationv1.TokenReviewStatus{
		Authenticated: true,
		User: authenticationv1.UserInfo{
			Username: "bedrock:alice",
			UID:      "uid-alice",
			Groups:   []string{"bedrock:bedrock-admins", "bedrock:ops"},
		},
	}
	cases := []struct {
		name  string
		token v1alpha1.APIToken
		user  v1alpha1.User
		want  authenticationv1.TokenReviewStatus
	}{
		{"no expiry", tokenFor("alice", nil), alice, valid},
		{"future expiry", tokenFor("alice", &future), alice, valid},
		{"expires now", tokenFor("alice", &exact), alice, authenticationv1.TokenReviewStatus{}},
		{"expired", tokenFor("alice", &past), alice, authenticationv1.TokenReviewStatus{}},
		{"disabled", tokenFor("alice", nil), disabled, authenticationv1.TokenReviewStatus{}},
		{"wrong user", tokenFor("bob", nil), alice, authenticationv1.TokenReviewStatus{}},
		{"empty user", tokenFor("", nil), v1alpha1.User{}, authenticationv1.TokenReviewStatus{}},
	}
	for _, tc := range cases {
		got := Review(tc.token, tc.user, []string{"ops", "bedrock-admins"}, now)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestTouchDue(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) *metav1.Time {
		value := metav1.NewTime(now.Add(-ago))
		return &value
	}
	cases := []struct {
		lastUsed *metav1.Time
		want     bool
	}{
		{nil, true},
		{at(0), false},
		{at(59 * time.Second), false},
		{at(time.Minute), true},
		{at(2 * time.Minute), true},
	}
	for _, tc := range cases {
		if got := TouchDue(tc.lastUsed, now); got != tc.want {
			t.Fatalf("TouchDue(%v) = %v, want %v", tc.lastUsed, got, tc.want)
		}
	}
}

type countingReader struct {
	client.Reader
	mu    sync.Mutex
	reads int
}

func (r *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.mu.Lock()
	r.reads++
	r.mu.Unlock()
	return r.Reader.Get(ctx, key, obj, opts...)
}

func (r *countingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.mu.Lock()
	r.reads++
	r.mu.Unlock()
	return r.Reader.List(ctx, list, opts...)
}

func (r *countingReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func startTestEnv(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "manifests", "00-crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	return c
}

func seed(t *testing.T, c client.Client) (v1alpha1.User, string) {
	t.Helper()
	ctx := context.Background()
	alice := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: release.SystemNamespace},
		Spec:       v1alpha1.UserSpec{Username: "alice", Methods: []string{v1alpha1.MethodPassword}},
	}
	ops := &v1alpha1.Group{
		ObjectMeta: metav1.ObjectMeta{Name: "ops", Namespace: release.SystemNamespace},
		Spec:       v1alpha1.GroupSpec{Members: []string{"alice"}},
	}
	token, err := secret.NewAPIToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	stored := &v1alpha1.APIToken{
		ObjectMeta: metav1.ObjectMeta{Name: secret.SHA256Hex(token), Namespace: release.SystemNamespace},
		Spec:       v1alpha1.APITokenSpec{UserRef: "alice", Description: "ci"},
	}
	for _, obj := range []client.Object{alice, ops, stored} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	return *alice, token
}

func serve(t *testing.T, reader client.Reader, writer client.Client, clock func() time.Time, bearer func(context.Context) (string, error)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(Handler(reader, writer, clock, bearer))
	t.Cleanup(server.Close)
	return server
}

func fixedBearer(context.Context) (string, error) {
	return testBearer, nil
}

func review(t *testing.T, server *httptest.Server, authorization, token string) (int, authenticationv1.TokenReview) {
	t.Helper()
	body, err := json.Marshal(authenticationv1.TokenReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"},
		Spec:     authenticationv1.TokenReviewSpec{Token: token},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/webhook/v1/tokenreview", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var answer authenticationv1.TokenReview
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, answer
}

func TestWebhookNeedsBearer(t *testing.T) {
	c := startTestEnv(t)
	_, token := seed(t, c)
	server := serve(t, c, c, time.Now, fixedBearer)
	for _, authorization := range []string{"", "Bearer wrong", "Bearer ", "Basic " + testBearer, testBearer} {
		if status, _ := review(t, server, authorization, token); status != http.StatusUnauthorized {
			t.Fatalf("authorization %q: status %d, want 401", authorization, status)
		}
	}
	status, answer := review(t, server, "Bearer "+testBearer, token)
	if status != http.StatusOK || !answer.Status.Authenticated || answer.APIVersion != "authentication.k8s.io/v1" || answer.Kind != "TokenReview" {
		t.Fatalf("status %d, answer %+v", status, answer)
	}
	broken := serve(t, c, c, time.Now, func(context.Context) (string, error) {
		return "", errors.New("secret bedrock-authn-webhook-token not found")
	})
	if status, _ := review(t, broken, "Bearer "+testBearer, token); status != http.StatusServiceUnavailable {
		t.Fatalf("unreadable bearer: status %d, want 503", status)
	}
	get, err := server.Client().Get(server.URL + "/webhook/v1/tokenreview")
	if err != nil {
		t.Fatal(err)
	}
	get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d", get.StatusCode)
	}
}

func TestWebhookIgnoresOtherTokens(t *testing.T) {
	c := startTestEnv(t)
	seed(t, c)
	reader := &countingReader{Reader: c}
	server := serve(t, reader, c, time.Now, fixedBearer)
	for _, token := range []string{"", "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2ln", "brk_short", "brk_0123456789abcdefghijABCDEFGHIJ012345678!", "BRK_0123456789abcdefghijABCDEFGHIJ0123456789"} {
		status, answer := review(t, server, "Bearer "+testBearer, token)
		if status != http.StatusOK || answer.Status.Authenticated || answer.Status.Error != "" {
			t.Fatalf("token %q: status %d, answer %+v", token, status, answer.Status)
		}
	}
	if reads := reader.count(); reads != 0 {
		t.Fatalf("a non-brk_ token must not reach the API, %d reads", reads)
	}
	unknown, err := secret.NewAPIToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, answer := review(t, server, "Bearer "+testBearer, unknown); answer.Status.Authenticated {
		t.Fatalf("an unknown token must fail: %+v", answer.Status)
	}
	if reads := reader.count(); reads != 1 {
		t.Fatalf("an unknown brk_ token needs exactly one read, got %d", reads)
	}
}

func TestWebhookRefusesDisabledUser(t *testing.T) {
	c := startTestEnv(t)
	alice, token := seed(t, c)
	server := serve(t, c, c, time.Now, fixedBearer)
	_, answer := review(t, server, "Bearer "+testBearer, token)
	want := authenticationv1.UserInfo{Username: "bedrock:alice", UID: string(alice.UID), Groups: []string{"bedrock:ops"}}
	if !answer.Status.Authenticated || !reflect.DeepEqual(answer.Status.User, want) {
		t.Fatalf("enabled user %+v, want %+v", answer.Status, want)
	}
	var stored v1alpha1.User
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: release.SystemNamespace, Name: "alice"}, &stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Disabled = true
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	if _, answer := review(t, server, "Bearer "+testBearer, token); answer.Status.Authenticated || answer.Status.User.Username != "" {
		t.Fatalf("disabled user %+v", answer.Status)
	}
	if err := c.Delete(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	if _, answer := review(t, server, "Bearer "+testBearer, token); answer.Status.Authenticated {
		t.Fatalf("deleted user %+v", answer.Status)
	}
}

func TestWebhookTouchesLastUsedOncePerMinute(t *testing.T) {
	c := startTestEnv(t)
	_, token := seed(t, c)
	start := time.Now().UTC().Truncate(time.Second)
	var mu sync.Mutex
	now := start
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	moveTo := func(at time.Time) {
		mu.Lock()
		defer mu.Unlock()
		now = at
	}
	server := serve(t, c, c, clock, fixedBearer)
	lastUsed := func() time.Time {
		var stored v1alpha1.APIToken
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: release.SystemNamespace, Name: secret.SHA256Hex(token)}, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Status.LastUsed == nil {
			return time.Time{}
		}
		return stored.Status.LastUsed.Time
	}
	steps := []struct {
		at   time.Time
		want time.Time
	}{
		{start, start},
		{start.Add(30 * time.Second), start},
		{start.Add(59 * time.Second), start},
		{start.Add(61 * time.Second), start.Add(61 * time.Second)},
	}
	for _, step := range steps {
		moveTo(step.at)
		if _, answer := review(t, server, "Bearer "+testBearer, token); !answer.Status.Authenticated {
			t.Fatalf("at %v: %+v", step.at, answer.Status)
		}
		if got := lastUsed(); !got.Equal(step.want) {
			t.Fatalf("at %v: lastUsed %v, want %v", step.at, got, step.want)
		}
	}
}
