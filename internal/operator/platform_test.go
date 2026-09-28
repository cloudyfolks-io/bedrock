package operator

import (
	"encoding/base64"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func platformInput(values map[string]string) AddonInput {
	settings := map[string]string{"platform.host": "", "platform.tls-mode": "SelfSigned", "letsencrypt.email": "", "letsencrypt.solver": "http01", "platform.custom-tls": ""}
	for k, v := range values {
		settings[k] = v
	}
	cluster := v1alpha1.Cluster{}
	cluster.Spec.API.VIP = "10.0.0.250"
	return AddonInput{Cluster: cluster, Settings: settings}
}

func TestRenderPlatformSelfSigned(t *testing.T) {
	out, err := RenderPlatform(platformInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	issuer := findObject(out.Objects, "ClusterIssuer", "bedrock-selfsigned")
	if issuer == nil || issuer.GetAPIVersion() != "cert-manager.io/v1" {
		t.Fatalf("objects %+v", out.Objects)
	}
	if _, found, _ := unstructured.NestedMap(issuer.Object, "spec", "selfSigned"); !found {
		t.Fatalf("issuer spec %+v", issuer.Object["spec"])
	}
	if findObject(out.Objects, "ClusterIssuer", "letsencrypt") != nil {
		t.Fatal("letsencrypt issuer must not be rendered in SelfSigned mode")
	}
	cert := findObject(out.Objects, "Certificate", "platform-tls")
	if cert == nil || cert.GetNamespace() != "traefik" {
		t.Fatalf("objects %+v", out.Objects)
	}
	names, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	secret, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	issuerName, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
	issuerKind, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "kind")
	want := []string{"10-0-0-250.sslip.io", "console.10-0-0-250.sslip.io", "sso.10-0-0-250.sslip.io", "api.10-0-0-250.sslip.io", "upload.10-0-0-250.sslip.io"}
	if len(names) != 5 || secret != "platform-tls" || issuerName != "bedrock-selfsigned" || issuerKind != "ClusterIssuer" {
		t.Fatalf("certificate spec %+v", cert.Object["spec"])
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("dnsNames %v", names)
		}
	}
	if len(out.Probes) != 1 || out.Probes[0].GVK.Kind != "Certificate" || out.Probes[0].Key.Namespace != "traefik" {
		t.Fatalf("probes %+v", out.Probes)
	}
}

func TestRenderPlatformLetsEncrypt(t *testing.T) {
	out, err := RenderPlatform(platformInput(map[string]string{"platform.tls-mode": "LetsEncrypt", "platform.host": "cloud.example.com", "letsencrypt.email": "ops@example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	issuer := findObject(out.Objects, "ClusterIssuer", "letsencrypt")
	server, _, _ := unstructured.NestedString(issuer.Object, "spec", "acme", "server")
	email, _, _ := unstructured.NestedString(issuer.Object, "spec", "acme", "email")
	key, _, _ := unstructured.NestedString(issuer.Object, "spec", "acme", "privateKeySecretRef", "name")
	solvers, _, _ := unstructured.NestedSlice(issuer.Object, "spec", "acme", "solvers")
	class, _, _ := unstructured.NestedString(solvers[0].(map[string]any), "http01", "ingress", "ingressClassName")
	if server != "https://acme-v02.api.letsencrypt.org/directory" || email != "ops@example.com" || key != "letsencrypt-account" || class != "traefik" {
		t.Fatalf("issuer spec %+v", issuer.Object["spec"])
	}
	cert := findObject(out.Objects, "Certificate", "platform-tls")
	issuerName, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
	names, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	if issuerName != "letsencrypt" || names[0] != "cloud.example.com" || names[1] != "console.cloud.example.com" {
		t.Fatalf("certificate spec %+v", cert.Object["spec"])
	}
}

func TestRenderPlatformLetsEncryptNeedsEmailAndHTTP01(t *testing.T) {
	if _, err := RenderPlatform(platformInput(map[string]string{"platform.tls-mode": "LetsEncrypt"})); err == nil {
		t.Fatal("missing email must fail")
	}
	if _, err := RenderPlatform(platformInput(map[string]string{"platform.tls-mode": "LetsEncrypt", "letsencrypt.email": "ops@example.com", "letsencrypt.solver": "dns01"})); err == nil {
		t.Fatal("dns01 must fail")
	}
}

func TestRenderPlatformCustom(t *testing.T) {
	in := platformInput(map[string]string{"platform.tls-mode": "Custom", "platform.custom-tls": "my-tls"})
	if _, err := RenderPlatform(in); err == nil {
		t.Fatal("missing secret must fail")
	}
	in.CustomTLS = &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("CERT"), "tls.key": []byte("KEY")}}
	out, err := RenderPlatform(in)
	if err != nil {
		t.Fatal(err)
	}
	secret := findObject(out.Objects, "Secret", "platform-tls")
	if secret == nil || secret.GetNamespace() != "traefik" {
		t.Fatalf("objects %+v", out.Objects)
	}
	kind, _, _ := unstructured.NestedString(secret.Object, "type")
	crt, _, _ := unstructured.NestedString(secret.Object, "data", "tls.crt")
	if kind != "kubernetes.io/tls" || crt != base64.StdEncoding.EncodeToString([]byte("CERT")) {
		t.Fatalf("secret %+v", secret.Object)
	}
	if findObject(out.Objects, "Certificate", "platform-tls") != nil {
		t.Fatal("custom mode must not render a Certificate")
	}
	if len(out.Probes) != 1 || out.Probes[0].GVK.Kind != "Secret" {
		t.Fatalf("probes %+v", out.Probes)
	}
}

func TestRenderPlatformCustomRequiresTLSKeys(t *testing.T) {
	in := platformInput(map[string]string{"platform.tls-mode": "Custom", "platform.custom-tls": "my-tls"})
	in.CustomTLS = &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("CERT")}}
	if _, err := RenderPlatform(in); err == nil {
		t.Fatal("secret missing tls.key must fail")
	}
	in.CustomTLS = &corev1.Secret{Data: map[string][]byte{"tls.key": []byte("KEY")}}
	if _, err := RenderPlatform(in); err == nil {
		t.Fatal("secret missing tls.crt must fail")
	}
}

func TestRenderPlatformRejectsUnknownMode(t *testing.T) {
	if _, err := RenderPlatform(platformInput(map[string]string{"platform.tls-mode": "Plain"})); err == nil {
		t.Fatal("unknown mode must fail")
	}
}

func TestDefaultAddonsOrder(t *testing.T) {
	addons := DefaultAddons()
	if len(addons) != 3 || addons[0].Name != "storage" || addons[1].Name != "virtualization" || addons[2].Name != "platform" {
		t.Fatalf("addons %+v", addons)
	}
}
