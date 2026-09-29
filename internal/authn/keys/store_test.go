package keys

import (
	"bytes"
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

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

func objectKey(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: release.SystemNamespace, Name: name}
}

func TestSaveAndLoadKeys(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	key, err := Generate(rand.Reader, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, c, key); err != nil {
		t.Fatal(err)
	}
	var signing v1alpha1.SigningKey
	if err := c.Get(ctx, objectKey(key.ID), &signing); err != nil {
		t.Fatal(err)
	}
	if signing.Spec.Algorithm != v1alpha1.AlgorithmES256 || signing.Spec.SecretRef != key.ID || !signing.Spec.NotBefore.Time.Equal(t0) || !signing.Spec.RetireAfter.Time.Equal(t0.Add(Lifetime)) || signing.Labels[v1alpha1.LabelKind] != "SigningKey" {
		t.Fatalf("signing key %+v", signing)
	}
	var stored corev1.Secret
	if err := c.Get(ctx, objectKey(key.ID), &stored); err != nil {
		t.Fatal(err)
	}
	owners := stored.OwnerReferences
	if stored.Labels[v1alpha1.LabelAuthn] != "true" || len(owners) != 1 || owners[0].Kind != "SigningKey" || owners[0].UID != signing.UID || owners[0].Controller == nil || !*owners[0].Controller {
		t.Fatalf("secret labels %v owners %+v", stored.Labels, owners)
	}
	if !bytes.HasPrefix(stored.Data["key.pem"], []byte("-----BEGIN PRIVATE KEY-----")) {
		t.Fatal("key.pem must be a PKCS#8 PEM block")
	}
	loaded, err := Load(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != key.ID || !loaded[0].NotBefore.Equal(t0) || !loaded[0].RetireAfter.Equal(key.RetireAfter) || !loaded[0].Private.Equal(key.Private) {
		t.Fatalf("loaded %+v", loaded)
	}
	orphan := v1alpha1.SigningKey{
		ObjectMeta: metav1.ObjectMeta{Name: "k-20261028t120000z", Namespace: release.SystemNamespace},
		Spec:       v1alpha1.SigningKeySpec{Algorithm: v1alpha1.AlgorithmES256, SecretRef: "k-20261028t120000z", NotBefore: metav1.NewTime(t0.Add(Lifetime)), RetireAfter: metav1.NewTime(t0.Add(2 * Lifetime))},
	}
	if err := c.Create(ctx, &orphan); err != nil {
		t.Fatal(err)
	}
	if loaded, err := Load(ctx, c); err != nil || len(loaded) != 1 {
		t.Fatalf("a key without its Secret must be skipped, got %d keys, err %v", len(loaded), err)
	}
	if err := Delete(ctx, c, key); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []client.Object{&v1alpha1.SigningKey{}, &corev1.Secret{}} {
		if err := c.Get(ctx, objectKey(key.ID), obj); err == nil {
			t.Fatalf("%T must be deleted", obj)
		}
	}
	if err := Delete(ctx, c, key); err != nil {
		t.Fatalf("deleting twice must not fail: %v", err)
	}
	if err := Delete(ctx, c, Key{ID: orphan.Name}); err != nil {
		t.Fatalf("deleting a key without its Secret must not fail: %v", err)
	}
}

func corruptSecret(t *testing.T, c client.Client, id string) {
	t.Helper()
	ctx := context.Background()
	var stored corev1.Secret
	if err := c.Get(ctx, objectKey(id), &stored); err != nil {
		t.Fatal(err)
	}
	stored.Data[secretKey] = []byte("not a pem block")
	if err := c.Update(ctx, &stored); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSkipsUndecodableSecret(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	good, err := Generate(rand.Reader, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, c, good); err != nil {
		t.Fatal(err)
	}
	bad, err := Generate(rand.Reader, t0.Add(Lifetime))
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, c, bad); err != nil {
		t.Fatal(err)
	}
	corruptSecret(t, c, bad.ID)
	loaded, err := Load(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != good.ID {
		t.Fatalf("Load must skip the undecodable key and keep the good one, got %v", ids(loaded))
	}
}

func TestNextNotBeforeHealsAfterOnlyKeyIsUndecodable(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	bad, err := Generate(rand.Reader, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, c, bad); err != nil {
		t.Fatal(err)
	}
	corruptSecret(t, c, bad.ID)
	loaded, err := Load(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 0 {
		t.Fatalf("the only key is undecodable, Load must return none, got %v", ids(loaded))
	}
	now := t0.Add(10 * day)
	next, ok := NextNotBefore(loaded, now)
	if !ok || !next.Equal(now) {
		t.Fatalf("with no usable keys the issuer must be told to mint one now, got %v %v", next, ok)
	}
}
