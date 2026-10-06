package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func startAuthnRBACEnv(t *testing.T) (client.Client, *rest.Config) {
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
	applyAuthnManifest(t, c, filepath.Join("..", "..", "..", "manifests", "85-authn", "10-rbac.yaml"))
	applyAuthnManifest(t, c, filepath.Join("..", "..", "..", "manifests", "85-authn", "20-secret-policy.yaml"))
	return c, cfg
}

func applyAuthnManifest(t *testing.T, c client.Client, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(obj.Object) == 0 {
			continue
		}
		if err := c.Create(context.Background(), &obj); err != nil {
			t.Fatalf("apply %s %s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
}

func authnServiceAccountClient(t *testing.T, cfg *rest.Config) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	impersonated := rest.CopyConfig(cfg)
	impersonated.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:bedrock-system:bedrock-authn",
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:bedrock-system", "system:authenticated"},
	}
	c, err := client.New(impersonated, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBedrockAuthnServiceAccountRBAC(t *testing.T) {
	admin, cfg := startAuthnRBACEnv(t)
	ctx := context.Background()
	create(t, admin, testUser("alice"), publicClient("bedrock-cli"))
	authn := authnServiceAccountClient(t, cfg)
	s := newTestStore(authn, testNow, testSettings(), nil)

	created, err := s.CreateAuthRequest(ctx, authorizeRequest("bedrock-cli"), "")
	if err != nil {
		t.Fatalf("the bedrock-authn service account must create an AuthRequest: %v", err)
	}
	id := created.GetID()
	if _, err := s.SaveLogin(ctx, id, finishLogin); err != nil {
		t.Fatalf("the bedrock-authn service account must update an AuthRequest: %v", err)
	}
	if err := s.DeleteAuthRequest(ctx, id); err != nil {
		t.Fatalf("the bedrock-authn service account must delete an AuthRequest: %v", err)
	}

	first := issue(t, s, loginRequest("alice", "bedrock-cli"))
	family := storedRefresh(t, admin, first).Spec.Family
	second, err := refresh(ctx, s, first)
	if err != nil {
		t.Fatalf("the bedrock-authn service account must rotate a refresh token: %v", err)
	}
	if _, err := refresh(ctx, s, first); !errors.Is(err, errRefreshReused) {
		t.Fatalf("a reused refresh token must revoke the family, got %v", err)
	}
	if left := familyTokens(t, admin, family); len(left) != 0 {
		t.Fatalf("revokeFamily needs deletecollection on refreshtokens, %d left", len(left))
	}
	if _, err := refresh(ctx, s, second); !isNotFound(err) {
		t.Fatalf("the newest token of a revoked family must stop working, got %v", err)
	}

	consoleToken := issue(t, s, loginRequest("alice", "console"))
	cookie, err := s.CreateSession(ctx, methods.Subject{User: *testUser("alice"), AMR: []string{"pwd"}}, "browser", "192.0.2.10")
	if err != nil {
		t.Fatalf("the bedrock-authn service account must create a Session: %v", err)
	}
	if _, err := s.SessionByCookie(ctx, cookie); err != nil {
		t.Fatalf("the bedrock-authn service account must update a Session: %v", err)
	}
	if err := s.TerminateSession(ctx, "alice", "console"); err != nil {
		t.Fatalf("TerminateSession needs deletecollection on sessions: %v", err)
	}
	if _, err := s.TokenRequestByRefreshToken(ctx, consoleToken); !isNotFound(err) {
		t.Fatalf("terminate session must delete the client's refresh token, got %v", err)
	}
	if _, err := s.SessionByCookie(ctx, cookie); !isNotFound(err) {
		t.Fatalf("terminate session must delete the user's sessions, got %v", err)
	}

	alice := *testUser("alice")
	if err := methods.SetPassword(ctx, authn, rand.Reader, alice, "first-passphrase"); err != nil {
		t.Fatalf("the bedrock-authn service account must create a Credential Secret: %v", err)
	}
	if err := methods.SetPassword(ctx, authn, rand.Reader, alice, "second-passphrase"); err != nil {
		t.Fatalf("the bedrock-authn service account must update a Credential Secret: %v", err)
	}
	credentialSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: release.SystemNamespace,
		Name:      v1alpha1.CredentialName(alice.Name, v1alpha1.MethodPassword),
	}}
	if err := authn.Delete(ctx, credentialSecret); err != nil {
		t.Fatalf("the bedrock-authn service account must delete a Credential Secret: %v", err)
	}
}
