package operator

import (
	"encoding/base64"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	authnName          = "bedrock-authn"
	cliClientID        = "bedrock-cli"
	consoleClientID    = "bedrock-console"
	consoleSecretName  = "bedrock-console-client"
	traefikAPIVersion  = "traefik.io/v1alpha1"
	authnServicePort   = int64(443)
	authnPlatformCAKey = "ca.crt"
)

var authnAddon = Addon{Name: "authn", Condition: v1alpha1.ConditionAuthnReady, Render: RenderAuthn}

var authnDeploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

func RenderAuthn(in AddonInput) (Rendered, error) {
	if !in.AuthnInstalled {
		return Rendered{SkipReason: "AuthnNotInstalled", SkipMessage: fmt.Sprintf("deployment %s/%s not found", release.SystemNamespace, authnName)}, nil
	}
	if in.WebhookToken == "" {
		return Rendered{SkipReason: "WaitingForWebhookToken", SkipMessage: fmt.Sprintf("secret %s/%s not found", release.SystemNamespace, apiserver.TokenSecretName)}, nil
	}
	mode := in.Settings["platform.tls-mode"]
	if mode == "" {
		mode = "SelfSigned"
	}
	if mode == "SelfSigned" && in.PlatformCA == nil {
		return Rendered{SkipReason: "WaitingForPlatformCA", SkipMessage: fmt.Sprintf("platform CA secret %s/%s not found", apiserver.CASecretNamespace, apiserver.CASecretName)}, nil
	}
	ca, err := authnCA(mode, in)
	if err != nil {
		return Rendered{}, err
	}
	host := settings.PlatformHost(in.Cluster.Spec.API.VIP, in.Settings["platform.host"])
	files := apiserver.Inputs{Host: host, CA: ca, Bearer: in.WebhookToken}
	authentication, err := apiserver.AuthenticationConfig(files)
	if err != nil {
		return Rendered{}, err
	}
	webhook, err := apiserver.WebhookKubeconfig(files)
	if err != nil {
		return Rendered{}, err
	}
	objects := append([]*unstructured.Unstructured{authnIngressRoute(host)}, BuiltinClients(host)...)
	objects = append(objects, authnConfigMap(authentication), authnWebhookSecret(webhook))
	probe := Probe{GVK: authnDeploymentGVK, Key: client.ObjectKey{Namespace: release.SystemNamespace, Name: authnName}, Gate: release.DefaultGate}
	return Rendered{Objects: objects, Probes: []Probe{probe}}, nil
}

func authnCA(mode string, in AddonInput) ([]byte, error) {
	switch mode {
	case "SelfSigned":
		return in.PlatformCA.Data[authnPlatformCAKey], nil
	case "LetsEncrypt":
		return nil, nil
	case "Custom":
		return in.CustomCA, nil
	}
	return nil, fmt.Errorf("platform.tls-mode %q is not supported", mode)
}

func BuiltinClients(host string) []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		authnObject(v1alpha1.GroupVersion.String(), "OAuthClient", cliClientID, map[string]any{"spec": map[string]any{
			"clientID":     cliClientID,
			"public":       true,
			"redirectURIs": []any{"http://127.0.0.1/callback"},
			"grantTypes":   []any{v1alpha1.GrantAuthorizationCode, v1alpha1.GrantRefreshToken, v1alpha1.GrantDeviceCode},
		}}),
		authnObject(v1alpha1.GroupVersion.String(), "OAuthClient", consoleClientID, map[string]any{"spec": map[string]any{
			"clientID":     consoleClientID,
			"public":       false,
			"redirectURIs": []any{"https://console." + host + "/oauth/callback"},
			"grantTypes":   []any{v1alpha1.GrantAuthorizationCode, v1alpha1.GrantRefreshToken},
			"secretRef":    consoleSecretName,
		}}),
	}
}

func authnIngressRoute(host string) *unstructured.Unstructured {
	service := map[string]any{"name": authnName, "port": authnServicePort, "scheme": "https", "serversTransport": authnName}
	return authnObject(traefikAPIVersion, "IngressRoute", authnName, map[string]any{"spec": map[string]any{
		"entryPoints": []any{"websecure"},
		"routes":      []any{map[string]any{"match": "Host(`sso." + host + "`)", "kind": "Rule", "services": []any{service}}},
		"tls":         map[string]any{},
	}})
}

func authnConfigMap(authentication []byte) *unstructured.Unstructured {
	return authnObject("v1", "ConfigMap", apiserver.ConfigMapName, map[string]any{"data": map[string]any{apiserver.AuthenticationFile: string(authentication)}})
}

func authnWebhookSecret(webhook []byte) *unstructured.Unstructured {
	return authnObject("v1", "Secret", apiserver.SecretName, map[string]any{
		"type": "Opaque",
		"data": map[string]any{apiserver.WebhookFile: base64.StdEncoding.EncodeToString(webhook)},
	})
}

func authnObject(apiVersion, kind, name string, fields map[string]any) *unstructured.Unstructured {
	content := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": release.SystemNamespace,
			"labels":    map[string]any{v1alpha1.LabelKind: kind, v1alpha1.LabelName: name},
		},
	}
	for key, value := range fields {
		content[key] = value
	}
	return &unstructured.Unstructured{Object: content}
}
