package operator

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	ProxyName           = "bedrock-proxy"
	PlatformCAConfigMap = "bedrock-platform-ca"
	proxyServicePort    = int64(443)
	proxyServingPort    = "https"
	proxyHealthzPath    = "/healthz"
	proxyReadyzPath     = "/readyz"
)

var proxyAddon = Addon{Name: "proxy", Condition: v1alpha1.ConditionProxyReady, Render: RenderProxy}

var proxyDeploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

func RenderProxy(in AddonInput) (Rendered, error) {
	if !in.ProxyInstalled {
		return Rendered{SkipReason: "ProxyNotInstalled", SkipMessage: fmt.Sprintf("deployment %s/%s not found", release.SystemNamespace, ProxyName)}, nil
	}
	host := in.Settings["platform.host"]
	if host == "" {
		return platformHostMissing(), nil
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
	objects := []*unstructured.Unstructured{
		proxyIngressRoute(host),
		proxyNetworkPolicy(joinCIDR(in.Cluster)),
		proxyPlatformCA(ca),
	}
	probe := Probe{GVK: proxyDeploymentGVK, Key: client.ObjectKey{Namespace: release.SystemNamespace, Name: ProxyName}, Gate: release.DefaultGate}
	return Rendered{Objects: objects, Probes: []Probe{probe}}, nil
}

func proxyIngressRoute(host string) *unstructured.Unstructured {
	service := map[string]any{"name": ProxyName, "port": proxyServicePort, "scheme": "https", "serversTransport": ProxyName}
	excluded := " && !Path(`" + proxyHealthzPath + "`) && !Path(`" + proxyReadyzPath + "`)"
	matches := []string{
		"Host(`console." + host + "`)" + excluded,
		"Host(`api." + host + "`)" + excluded,
		"Host(`" + host + "`)" + excluded,
	}
	routes := make([]any, 0, len(matches))
	for _, match := range matches {
		routes = append(routes, map[string]any{"match": match, "kind": "Rule", "services": []any{service}})
	}
	return authnObject(traefikAPIVersion, "IngressRoute", ProxyName, map[string]any{"spec": map[string]any{
		"entryPoints": []any{"websecure"},
		"routes":      routes,
		"tls":         map[string]any{},
	}})
}

func proxyNetworkPolicy(cidr string) *unstructured.Unstructured {
	toPort := map[string]any{"protocol": "TCP", "port": proxyServingPort}
	return authnObject("networking.k8s.io/v1", "NetworkPolicy", ProxyName, map[string]any{"spec": map[string]any{
		"podSelector": map[string]any{"matchLabels": map[string]any{"app": ProxyName}},
		"policyTypes": []any{"Ingress"},
		"ingress": []any{
			map[string]any{
				"from": []any{map[string]any{
					"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": traefikNamespace}},
					"podSelector":       map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "traefik"}},
				}},
				"ports": []any{toPort},
			},
			map[string]any{
				"from":  []any{map[string]any{"ipBlock": map[string]any{"cidr": cidr}}},
				"ports": []any{toPort},
			},
		},
	}})
}

func proxyPlatformCA(ca []byte) *unstructured.Unstructured {
	return authnObject("v1", "ConfigMap", PlatformCAConfigMap, map[string]any{"data": map[string]any{authnPlatformCAKey: string(ca)}})
}
