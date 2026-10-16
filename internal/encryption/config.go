package encryption

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiserverv1 "k8s.io/apiserver/pkg/apis/apiserver/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	Dir             = "/etc/bedrock/encryption"
	FileName        = "encryption.yaml"
	File            = Dir + "/" + FileName
	SecretNamespace = "kube-system"
	SecretName      = "bedrock-secrets-encryption"
	SecretKey       = "encryption.yaml"
	KMSProviderName = "bedrock-kms"
	ArgName         = "encryption-provider-config"
	JoinUser        = "bedrock-join"

	aesKeyName      = "key1"
	aesKeyBytes     = 32
	kmsAPIVersion   = "v2"
	secretsResource = "secrets"
	unixScheme      = "unix://"
)

func Args() map[string]string {
	return map[string]string{ArgName: File}
}

func NewConfig(random io.Reader, kms *v1alpha1.KMSSpec) ([]byte, error) {
	provider, err := newProvider(random, kms)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(apiserverv1.EncryptionConfiguration{
		TypeMeta: metav1.TypeMeta{APIVersion: "apiserver.config.k8s.io/v1", Kind: "EncryptionConfiguration"},
		Resources: []apiserverv1.ResourceConfiguration{{
			Resources: []string{secretsResource},
			Providers: []apiserverv1.ProviderConfiguration{provider},
		}},
	})
}

func newProvider(random io.Reader, kms *v1alpha1.KMSSpec) (apiserverv1.ProviderConfiguration, error) {
	if kms != nil {
		return kmsProvider(*kms), nil
	}
	return aesgcmProvider(random)
}

func kmsProvider(kms v1alpha1.KMSSpec) apiserverv1.ProviderConfiguration {
	return apiserverv1.ProviderConfiguration{KMS: &apiserverv1.KMSConfiguration{
		APIVersion: kmsAPIVersion,
		Name:       KMSProviderName,
		Endpoint:   kms.Endpoint,
	}}
}

func aesgcmProvider(random io.Reader) (apiserverv1.ProviderConfiguration, error) {
	key := make([]byte, aesKeyBytes)
	if _, err := io.ReadFull(random, key); err != nil {
		return apiserverv1.ProviderConfiguration{}, fmt.Errorf("read %d random bytes for the encryption key: %w", aesKeyBytes, err)
	}
	return apiserverv1.ProviderConfiguration{AESGCM: &apiserverv1.AESConfiguration{
		Keys: []apiserverv1.Key{{Name: aesKeyName, Secret: base64.StdEncoding.EncodeToString(key)}},
	}}, nil
}

func SocketPaths(content []byte) ([]string, error) {
	var cfg apiserverv1.EncryptionConfiguration
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return nil, fmt.Errorf("parse encryption config: %w", err)
	}
	entries := secretsEntries(cfg)
	if len(entries) == 0 {
		return nil, fmt.Errorf("encryption config has no provider for secrets")
	}
	paths := []string{}
	for _, entry := range entries {
		for _, provider := range entry.Providers {
			if provider.KMS == nil {
				continue
			}
			if path, ok := strings.CutPrefix(provider.KMS.Endpoint, unixScheme); ok {
				paths = append(paths, path)
			}
		}
	}
	return paths, nil
}

func secretsEntries(cfg apiserverv1.EncryptionConfiguration) []apiserverv1.ResourceConfiguration {
	return slices.DeleteFunc(slices.Clone(cfg.Resources), func(entry apiserverv1.ResourceConfiguration) bool {
		return !slices.Contains(entry.Resources, secretsResource)
	})
}

func Secret(content []byte) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Namespace: SecretNamespace, Name: SecretName},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{SecretKey: content},
	}
}

func ReadSecret(ctx context.Context, c client.Reader) ([]byte, error) {
	key := client.ObjectKey{Namespace: SecretNamespace, Name: SecretName}
	var secret corev1.Secret
	if err := c.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("read secret %s: %w", key, err)
	}
	content := secret.Data[SecretKey]
	if len(content) == 0 {
		return nil, fmt.Errorf("secret %s holds no %s", key, SecretKey)
	}
	return content, nil
}
