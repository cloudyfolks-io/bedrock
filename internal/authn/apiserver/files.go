package apiserver

import (
	"crypto/sha256"
	"encoding/hex"
	"path"

	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	Dir                = "/etc/bedrock/authn"
	AuthenticationFile = "authentication.yaml"
	WebhookFile        = "webhook.kubeconfig"
	ConfigMapName      = "bedrock-authn-apiserver"
	SecretName         = "bedrock-authn-webhook"
	TokenSecretName    = "bedrock-authn-webhook-token"
	audience           = "bedrock"
	webhookCluster     = "bedrock-authn"
	webhookUser        = "kube-apiserver"
	webhookPath        = "/webhook/v1/tokenreview"
)

type Inputs struct {
	Host   string
	CA     []byte
	Bearer string
}

func AuthenticationConfig(in Inputs) ([]byte, error) {
	return sigyaml.Marshal(map[string]any{
		"apiVersion": "apiserver.config.k8s.io/v1",
		"kind":       "AuthenticationConfiguration",
		"jwt": []any{map[string]any{
			"issuer": issuer(in),
			"claimMappings": map[string]any{
				"username": map[string]any{"claim": "sub", "prefix": v1alpha1.AuthnPrefix},
				"groups":   map[string]any{"claim": "groups", "prefix": v1alpha1.AuthnPrefix},
			},
		}},
	})
}

func issuer(in Inputs) map[string]any {
	fields := map[string]any{"url": ssoURL(in.Host), "audiences": []any{audience}}
	if len(in.CA) == 0 {
		return fields
	}
	fields["certificateAuthority"] = string(in.CA)
	return fields
}

func ssoURL(host string) string {
	return "https://sso." + host
}

func WebhookKubeconfig(in Inputs) ([]byte, error) {
	return sigyaml.Marshal(clientcmdv1.Config{
		APIVersion:     "v1",
		Kind:           "Config",
		Clusters:       []clientcmdv1.NamedCluster{{Name: webhookCluster, Cluster: clientcmdv1.Cluster{Server: ssoURL(in.Host) + webhookPath, CertificateAuthorityData: in.CA}}},
		AuthInfos:      []clientcmdv1.NamedAuthInfo{{Name: webhookUser, AuthInfo: clientcmdv1.AuthInfo{Token: in.Bearer}}},
		Contexts:       []clientcmdv1.NamedContext{{Name: webhookCluster, Context: clientcmdv1.Context{Cluster: webhookCluster, AuthInfo: webhookUser}}},
		CurrentContext: webhookCluster,
	})
}

func Args() map[string]string {
	return map[string]string{
		"authentication-config":                    path.Join(Dir, AuthenticationFile),
		"authentication-token-webhook-config-file": path.Join(Dir, WebhookFile),
		"authentication-token-webhook-cache-ttl":   "30s",
		"authentication-token-webhook-version":     "v1",
	}
}

func Hash(authentication, webhook []byte) string {
	sum := sha256.New()
	sum.Write(authentication)
	sum.Write([]byte{0})
	sum.Write(webhook)
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))
}
