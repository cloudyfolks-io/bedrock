package operator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

var (
	caKey    = client.ObjectKey{Namespace: apiserver.CASecretNamespace, Name: apiserver.CASecretName}
	tokenKey = client.ObjectKey{Namespace: release.SystemNamespace, Name: apiserver.TokenSecretName}
)

func authnNamespaces(t *testing.T, c client.Client) {
	t.Helper()
	for _, name := range []string{apiserver.CASecretNamespace, release.SystemNamespace} {
		if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
}

func readSecret(t *testing.T, c client.Client, key client.ObjectKey) corev1.Secret {
	t.Helper()
	var secret corev1.Secret
	if err := c.Get(context.Background(), key, &secret); err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestEnsureAuthnMaterialCreatesAbsentSecrets(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	if err := ensureAuthnMaterial(context.Background(), c, rand.Reader, now); err != nil {
		t.Fatal(err)
	}
	ca := readSecret(t, c, caKey)
	if ca.Type != corev1.SecretTypeTLS || ca.Labels[v1alpha1.LabelKind] != "PlatformCA" || ca.Labels[v1alpha1.LabelName] != apiserver.CASecretName {
		t.Fatalf("CA secret type %s labels %v", ca.Type, ca.Labels)
	}
	if !bytes.Equal(ca.Data["ca.crt"], ca.Data["tls.crt"]) {
		t.Fatal("ca.crt and tls.crt must hold the same certificate")
	}
	pair, err := tls.X509KeyPair(ca.Data["tls.crt"], ca.Data["tls.key"])
	if err != nil {
		t.Fatal(err)
	}
	if !pair.Leaf.IsCA || !pair.Leaf.NotAfter.Equal(now.AddDate(10, 0, 0)) {
		t.Fatalf("CA %v valid until %v", pair.Leaf.IsCA, pair.Leaf.NotAfter)
	}
	token := readSecret(t, c, tokenKey)
	want := map[string]string{v1alpha1.LabelAuthn: "true", v1alpha1.LabelKind: "WebhookToken", v1alpha1.LabelName: apiserver.TokenSecretName}
	for key, value := range want {
		if token.Labels[key] != value {
			t.Fatalf("token labels %v", token.Labels)
		}
	}
	if len(token.Data["token"]) != 32 || token.Type != corev1.SecretTypeOpaque {
		t.Fatalf("token type %s length %d", token.Type, len(token.Data["token"]))
	}
	for _, secret := range []corev1.Secret{ca, token} {
		if len(secret.ManagedFields) != 1 || secret.ManagedFields[0].Manager != v1alpha1.AuthnFieldManager {
			t.Fatalf("%s field managers %v", secret.Name, secret.ManagedFields)
		}
	}
}

func TestEnsureAuthnMaterialKeepsPresentSecrets(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	ctx := context.Background()
	existing := []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Namespace: caKey.Namespace, Name: caKey.Name}, Data: map[string][]byte{"ca.crt": []byte("OLD-CA"), "tls.crt": []byte("OLD-CA"), "tls.key": []byte("OLD-KEY")}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: tokenKey.Namespace, Name: tokenKey.Name}, Data: map[string][]byte{"token": []byte("old-bearer")}},
	}
	for _, secret := range existing {
		if err := c.Create(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	before := map[client.ObjectKey]string{caKey: readSecret(t, c, caKey).ResourceVersion, tokenKey: readSecret(t, c, tokenKey).ResourceVersion}
	if err := ensureAuthnMaterial(ctx, c, rand.Reader, time.Now()); err != nil {
		t.Fatal(err)
	}
	for key, version := range before {
		if got := readSecret(t, c, key); got.ResourceVersion != version {
			t.Fatalf("%s changed: %v", key, got.Data)
		}
	}
}

func TestEnsureAuthnMaterialCreatesOnlyTheMissingSecret(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: tokenKey.Namespace, Name: tokenKey.Name}, Data: map[string][]byte{"token": []byte("old-bearer")}}); err != nil {
		t.Fatal(err)
	}
	if err := ensureAuthnMaterial(ctx, c, rand.Reader, time.Now()); err != nil {
		t.Fatal(err)
	}
	if string(readSecret(t, c, tokenKey).Data["token"]) != "old-bearer" {
		t.Fatal("the present token must stay")
	}
	if len(readSecret(t, c, caKey).Data["tls.key"]) == 0 {
		t.Fatal("the absent CA must be created")
	}
}

func TestEnsureAuthnMaterialWaitsForNamespaces(t *testing.T) {
	c, _ := StartTestEnv(t)
	if err := ensureAuthnMaterial(context.Background(), c, rand.Reader, time.Now()); err != nil {
		t.Fatalf("a missing namespace is not an error yet: %v", err)
	}
}

func TestEnsureAuthnMaterialConcurrentCreatesKeepOneWinner(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ensureAuthnMaterial(ctx, c, rand.Reader, time.Now())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a losing writer must accept the winner: %v", err)
		}
	}
	ca := readSecret(t, c, caKey)
	if _, err := tls.X509KeyPair(ca.Data["tls.crt"], ca.Data["tls.key"]); err != nil {
		t.Fatalf("the winning CA must be one whole key pair: %v", err)
	}
}

type raceClient struct {
	client.Client
	afterGet func()
	once     sync.Once
}

func (r *raceClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := r.Client.Get(ctx, key, obj, opts...)
	r.once.Do(r.afterGet)
	return err
}

func TestEnsureAuthnMaterialAcceptsAWriterBetweenReadAndCreate(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	ctx := context.Background()
	winner := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: caKey.Namespace, Name: caKey.Name}, Data: map[string][]byte{"ca.crt": []byte("WINNER"), "tls.crt": []byte("WINNER"), "tls.key": []byte("WINNER-KEY")}}
	racing := &raceClient{Client: c, afterGet: func() {
		if err := c.Create(ctx, winner.DeepCopy()); err != nil {
			t.Error(err)
		}
	}}
	if err := ensureAuthnMaterial(ctx, racing, rand.Reader, time.Now()); err != nil {
		t.Fatal(err)
	}
	if string(readSecret(t, c, caKey).Data["tls.key"]) != "WINNER-KEY" {
		t.Fatal("the create that lost must not overwrite the winner")
	}
}

func TestEnsureAuthnMaterialReportsReadErrors(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	broken := errors.New("read failed")
	failing := &failingGetClient{Client: c, err: broken}
	if err := ensureAuthnMaterial(context.Background(), failing, rand.Reader, time.Now()); !errors.Is(err, broken) {
		t.Fatalf("error %v", err)
	}
}

type failingGetClient struct {
	client.Client
	err error
}

func (f *failingGetClient) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return f.err
}

func TestAddonReconcileEnsuresAuthnMaterial(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	ctx := context.Background()
	createClusterWithStatus(t, ctx, c, "v1", v1alpha1.ClusterStatus{Version: "v1"})
	r := &AddonReconciler{Client: c, Interval: time.Second, ReadyInterval: time.Second}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: v1alpha1.ClusterName}}); err != nil {
		t.Fatal(err)
	}
	readSecret(t, c, caKey)
	readSecret(t, c, tokenKey)
}

func TestComponentHooksEnsureAuthnMaterialBeforeTheAuthnGroup(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	ctx := context.Background()
	before := authnBootstrapHook(c)
	if err := before(ctx, release.Group{Name: "traefik"}); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := c.Get(ctx, caKey, &secret); err == nil {
		t.Fatal("only the authn group needs the material")
	}
	if err := before(ctx, release.Group{Name: authnGroup}); err != nil {
		t.Fatal(err)
	}
	readSecret(t, c, caKey)
	readSecret(t, c, tokenKey)
}

func TestInstallEnsuresAuthnMaterialBeforeTheAuthnGroup(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	ctx := context.Background()
	createClusterWithStatus(t, ctx, c, "dev", v1alpha1.ClusterStatus{})
	bundle := testBundle(t)
	bundle.Groups[len(bundle.Groups)-1].Name = authnGroup
	r := &ClusterReconciler{Client: c, Bundle: bundle, Gates: release.Gates{}, Interval: 100 * time.Millisecond, GroupTimeout: 10 * time.Second}
	vars, err := release.Vars(ctx, c, "10.0.0.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.install(ctx, 1, vars); err != nil {
		t.Fatal(err)
	}
	readSecret(t, c, caKey)
	readSecret(t, c, tokenKey)
}
