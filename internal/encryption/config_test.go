package encryption

import (
	"bytes"
	"context"
	"encoding/base64"
	"maps"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apiserverv1 "k8s.io/apiserver/pkg/apis/apiserver/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const kmsEndpoint = "unix:///run/kms/kms.sock"

func decode(t *testing.T, content []byte) apiserverv1.EncryptionConfiguration {
	t.Helper()
	var cfg apiserverv1.EncryptionConfiguration
	if err := yaml.UnmarshalStrict(content, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func secretsProviders(t *testing.T, cfg apiserverv1.EncryptionConfiguration) []apiserverv1.ProviderConfiguration {
	t.Helper()
	if len(cfg.Resources) != 1 || len(cfg.Resources[0].Resources) != 1 || cfg.Resources[0].Resources[0] != "secrets" {
		t.Fatalf("unexpected resource entries %+v", cfg.Resources)
	}
	return cfg.Resources[0].Providers
}

func TestNewConfigAESGCM(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	content, err := NewConfig(bytes.NewReader(key), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, content)
	if cfg.APIVersion != "apiserver.config.k8s.io/v1" || cfg.Kind != "EncryptionConfiguration" {
		t.Fatalf("unexpected type meta %+v", cfg.TypeMeta)
	}
	providers := secretsProviders(t, cfg)
	if len(providers) != 1 {
		t.Fatalf("want one provider, got %+v", providers)
	}
	p := providers[0]
	if p.Identity != nil || p.KMS != nil || p.AESCBC != nil || p.Secretbox != nil || p.AESGCM == nil {
		t.Fatalf("want only aesgcm, got %+v", p)
	}
	want := []apiserverv1.Key{{Name: "key1", Secret: base64.StdEncoding.EncodeToString(key)}}
	if len(p.AESGCM.Keys) != 1 || p.AESGCM.Keys[0] != want[0] {
		t.Fatalf("unexpected keys %+v", p.AESGCM.Keys)
	}
}

func TestNewConfigShortRandom(t *testing.T) {
	if _, err := NewConfig(bytes.NewReader(bytes.Repeat([]byte{7}, 31)), nil); err == nil {
		t.Fatal("a 31 byte random source must fail")
	}
}

func TestNewConfigKMS(t *testing.T) {
	content, err := NewConfig(nil, &v1alpha1.KMSSpec{Endpoint: kmsEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	providers := secretsProviders(t, decode(t, content))
	if len(providers) != 1 {
		t.Fatalf("want one provider, got %+v", providers)
	}
	p := providers[0]
	if p.AESGCM != nil || p.Identity != nil || p.KMS == nil {
		t.Fatalf("want only kms, got %+v", p)
	}
	if p.KMS.APIVersion != "v2" || p.KMS.Name != "bedrock-kms" || p.KMS.Endpoint != kmsEndpoint {
		t.Fatalf("unexpected kms provider %+v", p.KMS)
	}
	if p.KMS.Timeout != nil || p.KMS.CacheSize != nil {
		t.Fatalf("timeout and cache size must stay unset: %+v", p.KMS)
	}
}

func TestSocketPaths(t *testing.T) {
	kms, err := NewConfig(nil, &v1alpha1.KMSSpec{Endpoint: kmsEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := SocketPaths(kms)
	if err != nil || len(paths) != 1 || paths[0] != "/run/kms/kms.sock" {
		t.Fatalf("SocketPaths(kms) = %v, %v", paths, err)
	}

	local, err := NewConfig(bytes.NewReader(bytes.Repeat([]byte{1}, 32)), nil)
	if err != nil {
		t.Fatal(err)
	}
	paths, err = SocketPaths(local)
	if err != nil || len(paths) != 0 {
		t.Fatalf("SocketPaths(aesgcm) = %v, %v", paths, err)
	}

	if _, err := SocketPaths([]byte("kind: x")); err == nil || !strings.Contains(err.Error(), "encryption config has no provider for secrets") {
		t.Fatalf("missing secrets entry error = %v", err)
	}
	if _, err := SocketPaths([]byte("{")); err == nil || !strings.HasPrefix(err.Error(), "parse encryption config") {
		t.Fatalf("malformed config error = %v", err)
	}
}

func TestPathAndArgs(t *testing.T) {
	if got := Path("/var/lib/k0s"); got != "/var/lib/k0s/pki/encryption.yaml" {
		t.Fatalf("Path = %q", got)
	}
	want := map[string]string{"encryption-provider-config": "/var/lib/k0s/pki/encryption.yaml"}
	if got := Args("/var/lib/k0s"); !maps.Equal(got, want) {
		t.Fatalf("Args = %v", got)
	}
}

func TestSecretRoundTrip(t *testing.T) {
	content := []byte("apiVersion: apiserver.config.k8s.io/v1\n")
	secret := Secret(content)
	if secret.Namespace != "kube-system" || secret.Name != "bedrock-secrets-encryption" || secret.Type != corev1.SecretTypeOpaque {
		t.Fatalf("unexpected secret %+v", secret.ObjectMeta)
	}
	if !bytes.Equal(secret.Data["encryption.yaml"], content) {
		t.Fatalf("unexpected data %v", secret.Data)
	}
	if secret.APIVersion != "v1" || secret.Kind != "Secret" {
		t.Fatalf("unexpected type meta %+v", secret.TypeMeta)
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := func(objects ...*corev1.Secret) *fake.ClientBuilder {
		builder := fake.NewClientBuilder().WithScheme(scheme)
		for _, o := range objects {
			builder = builder.WithObjects(o)
		}
		return builder
	}

	got, err := ReadSecret(context.Background(), reader(secret).Build())
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("ReadSecret = %q, %v", got, err)
	}

	if _, err := ReadSecret(context.Background(), reader().Build()); err == nil || !strings.Contains(err.Error(), "kube-system/bedrock-secrets-encryption") {
		t.Fatalf("missing secret error = %v", err)
	}

	empty := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "bedrock-secrets-encryption"},
		Data:       map[string][]byte{"encryption.yaml": {}},
	}
	if _, err := ReadSecret(context.Background(), reader(empty).Build()); err == nil || !strings.Contains(err.Error(), "holds no encryption.yaml") {
		t.Fatalf("empty secret error = %v", err)
	}
}
