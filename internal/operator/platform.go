package operator

import (
	"encoding/base64"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	traefikNamespace  = "traefik"
	platformTLSSecret = "platform-tls"
	issuerSelfSigned  = "bedrock-selfsigned"
	issuerLetsEncrypt = "letsencrypt"
	acmeServer        = "https://acme-v02.api.letsencrypt.org/directory"
)

var platformAddon = Addon{Name: "platform", Condition: v1alpha1.ConditionPlatformReady, Render: RenderPlatform}

var (
	certificateGVK = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	secretGVK      = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}
)

func DefaultAddons() []Addon {
	return []Addon{storageAddon, virtualizationAddon, platformAddon}
}

func PlatformHost(vip, host string) string {
	if host != "" {
		return host
	}
	return strings.ReplaceAll(vip, ".", "-") + ".sslip.io"
}

func RenderPlatform(in AddonInput) (Rendered, error) {
	host := PlatformHost(in.Cluster.Spec.API.VIP, in.Settings["platform.host"])
	mode := in.Settings["platform.tls-mode"]
	if mode == "" {
		mode = "SelfSigned"
	}
	objects := []*unstructured.Unstructured{clusterIssuer(issuerSelfSigned, map[string]any{"selfSigned": map[string]any{}})}
	certificateProbe := Probe{GVK: certificateGVK, Key: client.ObjectKey{Namespace: traefikNamespace, Name: platformTLSSecret}, Gate: release.ConditionGate("Ready")}
	switch mode {
	case "SelfSigned":
		objects = append(objects, certificate(host, issuerSelfSigned))
		return Rendered{Objects: objects, Probes: []Probe{certificateProbe}}, nil
	case "LetsEncrypt":
		issuer, err := letsEncryptIssuer(in.Settings["letsencrypt.email"], in.Settings["letsencrypt.solver"])
		if err != nil {
			return Rendered{}, err
		}
		objects = append(objects, issuer, certificate(host, issuerLetsEncrypt))
		return Rendered{Objects: objects, Probes: []Probe{certificateProbe}}, nil
	case "Custom":
		secret, err := customSecret(in.Settings["platform.custom-tls"], in)
		if err != nil {
			return Rendered{}, err
		}
		probe := Probe{GVK: secretGVK, Key: client.ObjectKey{Namespace: traefikNamespace, Name: platformTLSSecret}, Gate: release.DefaultGate}
		return Rendered{Objects: append(objects, secret), Probes: []Probe{probe}}, nil
	}
	return Rendered{}, fmt.Errorf("platform.tls-mode %q is not supported", mode)
}

func clusterIssuer(name string, spec map[string]any) *unstructured.Unstructured {
	return object("cert-manager.io/v1", "ClusterIssuer", "", name, spec)
}

func letsEncryptIssuer(email, solver string) (*unstructured.Unstructured, error) {
	if email == "" {
		return nil, fmt.Errorf("letsencrypt.email is required for tls mode LetsEncrypt")
	}
	if solver != "" && solver != "http01" {
		return nil, fmt.Errorf("letsencrypt.solver %q is not supported yet", solver)
	}
	return clusterIssuer(issuerLetsEncrypt, map[string]any{"acme": map[string]any{
		"server":              acmeServer,
		"email":               email,
		"privateKeySecretRef": map[string]any{"name": "letsencrypt-account"},
		"solvers":             []any{map[string]any{"http01": map[string]any{"ingress": map[string]any{"ingressClassName": "traefik"}}}},
	}}), nil
}

func certificate(host, issuer string) *unstructured.Unstructured {
	names := []any{host}
	for _, prefix := range []string{"console", "sso", "api", "upload"} {
		names = append(names, prefix+"."+host)
	}
	return object("cert-manager.io/v1", "Certificate", traefikNamespace, platformTLSSecret, map[string]any{
		"secretName": platformTLSSecret,
		"dnsNames":   names,
		"issuerRef":  map[string]any{"kind": "ClusterIssuer", "name": issuer},
		"privateKey": map[string]any{"rotationPolicy": "Always"},
	})
}

func customSecret(name string, in AddonInput) (*unstructured.Unstructured, error) {
	if name == "" {
		return nil, fmt.Errorf("platform.custom-tls is required for tls mode Custom")
	}
	if in.CustomTLS == nil {
		return nil, fmt.Errorf("secret %s/%s not found", release.SystemNamespace, name)
	}
	crt := in.CustomTLS.Data["tls.crt"]
	key := in.CustomTLS.Data["tls.key"]
	if len(crt) == 0 || len(key) == 0 {
		return nil, fmt.Errorf("secret %s/%s must contain tls.crt and tls.key", release.SystemNamespace, name)
	}
	data := map[string]any{
		"tls.crt": base64.StdEncoding.EncodeToString(crt),
		"tls.key": base64.StdEncoding.EncodeToString(key),
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": platformTLSSecret, "namespace": traefikNamespace},
		"type":       "kubernetes.io/tls",
		"data":       data,
	}}, nil
}
