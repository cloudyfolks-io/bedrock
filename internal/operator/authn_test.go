package operator

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const testPlatformCA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func authnInput(host, mode string) AddonInput {
	cluster := v1alpha1.Cluster{}
	cluster.Spec.API.VIP = "10.0.0.250"
	settings := map[string]string{"platform.host": host, "platform.tls-mode": mode, "platform.custom-tls": "my-tls", "letsencrypt.email": "ops@example.com", "letsencrypt.solver": "http01"}
	ca := &corev1.Secret{Data: map[string][]byte{"ca.crt": []byte(testPlatformCA)}}
	return AddonInput{Cluster: cluster, Settings: settings, PlatformCA: ca, WebhookToken: "bearer-1", AuthnInstalled: true}
}

func renderedAuthnFiles(t *testing.T, out Rendered) (string, []byte) {
	t.Helper()
	cm := findObject(out.Objects, "ConfigMap", apiserver.ConfigMapName)
	secret := findObject(out.Objects, "Secret", apiserver.SecretName)
	if cm == nil || secret == nil {
		t.Fatalf("objects %+v", out.Objects)
	}
	authentication, _, _ := unstructured.NestedString(cm.Object, "data", apiserver.AuthenticationFile)
	encoded, _, _ := unstructured.NestedString(secret.Object, "data", apiserver.WebhookFile)
	webhook, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return authentication, webhook
}

func TestRenderAuthnObjects(t *testing.T) {
	out, err := RenderAuthn(authnInput("lab.example", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipReason != "" || len(out.Objects) != 5 {
		t.Fatalf("rendered %+v", out)
	}
	for _, obj := range out.Objects {
		if obj.GetNamespace() != release.SystemNamespace || obj.GetLabels()[v1alpha1.LabelKind] != obj.GetKind() || obj.GetLabels()[v1alpha1.LabelName] != obj.GetName() {
			t.Fatalf("object %s %s meta %s %v", obj.GetKind(), obj.GetName(), obj.GetNamespace(), obj.GetLabels())
		}
	}
	secret := findObject(out.Objects, "Secret", apiserver.SecretName)
	if secret.GetLabels()[v1alpha1.LabelAuthn] != "true" {
		t.Fatalf("the webhook Secret must carry the authn label: %v", secret.GetLabels())
	}
	if cm := findObject(out.Objects, "ConfigMap", apiserver.ConfigMapName); cm.GetLabels()[v1alpha1.LabelAuthn] != "" {
		t.Fatalf("the ConfigMap must not carry the authn label: %v", cm.GetLabels())
	}
	route := findObject(out.Objects, "IngressRoute", "bedrock-authn")
	if route == nil || route.GetAPIVersion() != "traefik.io/v1alpha1" {
		t.Fatalf("objects %+v", out.Objects)
	}
	entryPoints, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
	routes, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
	tls, found, _ := unstructured.NestedMap(route.Object, "spec", "tls")
	if len(entryPoints) != 1 || entryPoints[0] != "websecure" || len(routes) != 1 || !found || len(tls) != 0 {
		t.Fatalf("route spec %+v", route.Object["spec"])
	}
	match, _, _ := unstructured.NestedString(routes[0].(map[string]any), "match")
	services, _, _ := unstructured.NestedSlice(routes[0].(map[string]any), "services")
	service := services[0].(map[string]any)
	if match != "Host(`sso.lab.example`)" || service["name"] != "bedrock-authn" || service["port"] != int64(443) || service["scheme"] != "https" || service["serversTransport"] != "bedrock-authn" {
		t.Fatalf("route %+v", routes[0])
	}
	cli := findObject(out.Objects, "OAuthClient", "bedrock-cli")
	public, _, _ := unstructured.NestedBool(cli.Object, "spec", "public")
	redirects, _, _ := unstructured.NestedStringSlice(cli.Object, "spec", "redirectURIs")
	grants, _, _ := unstructured.NestedStringSlice(cli.Object, "spec", "grantTypes")
	if !public || len(redirects) != 1 || redirects[0] != "http://127.0.0.1/callback" || strings.Join(grants, ",") != "authorization_code,refresh_token,urn:ietf:params:oauth:grant-type:device_code" {
		t.Fatalf("bedrock-cli %+v", cli.Object["spec"])
	}
	console := findObject(out.Objects, "OAuthClient", "bedrock-console")
	public, _, _ = unstructured.NestedBool(console.Object, "spec", "public")
	redirects, _, _ = unstructured.NestedStringSlice(console.Object, "spec", "redirectURIs")
	secretRef, _, _ := unstructured.NestedString(console.Object, "spec", "secretRef")
	if public || len(redirects) != 1 || redirects[0] != "https://console.lab.example/oauth/callback" || secretRef != "bedrock-console-client" {
		t.Fatalf("bedrock-console %+v", console.Object["spec"])
	}
	authentication, webhook := renderedAuthnFiles(t, out)
	in := apiserver.Inputs{Host: "lab.example", CA: []byte(testPlatformCA), Bearer: "bearer-1"}
	wantAuthentication, err := apiserver.AuthenticationConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	wantWebhook, err := apiserver.WebhookKubeconfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if authentication != string(wantAuthentication) || string(webhook) != string(wantWebhook) {
		t.Fatalf("files\n%s\n%s", authentication, webhook)
	}
	if len(out.Probes) != 1 || out.Probes[0].GVK.Kind != "Deployment" || out.Probes[0].Key.Name != "bedrock-authn" || out.Probes[0].Key.Namespace != release.SystemNamespace {
		t.Fatalf("probes %+v", out.Probes)
	}
}

func TestAuthnAddonFollowsPlatformHost(t *testing.T) {
	before, err := RenderAuthn(authnInput("old.example", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := RenderAuthn(authnInput("new.example", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	oldAuthentication, oldWebhook := renderedAuthnFiles(t, before)
	authentication, webhook := renderedAuthnFiles(t, after)
	if !strings.Contains(authentication, "url: https://sso.new.example") || strings.Contains(authentication, "old.example") {
		t.Fatalf("the issuer must follow platform.host:\n%s", authentication)
	}
	config, err := clientcmd.Load(webhook)
	if err != nil {
		t.Fatal(err)
	}
	oldConfig, err := clientcmd.Load(oldWebhook)
	if err != nil {
		t.Fatal(err)
	}
	if config.Clusters["bedrock-authn"].Server != "https://sso.new.example/webhook/v1/tokenreview" {
		t.Fatalf("webhook server %s", config.Clusters["bedrock-authn"].Server)
	}
	if config.AuthInfos["kube-apiserver"].Token != oldConfig.AuthInfos["kube-apiserver"].Token || string(config.Clusters["bedrock-authn"].CertificateAuthorityData) != string(oldConfig.Clusters["bedrock-authn"].CertificateAuthorityData) {
		t.Fatal("a host change keeps the bearer and the CA")
	}
	if oldAuthentication == authentication {
		t.Fatal("the rendered file must change with the host")
	}
	match, _, _ := unstructured.NestedSlice(findObject(after.Objects, "IngressRoute", "bedrock-authn").Object, "spec", "routes")
	if match[0].(map[string]any)["match"] != "Host(`sso.new.example`)" {
		t.Fatalf("route %+v", match)
	}
	redirects, _, _ := unstructured.NestedStringSlice(findObject(after.Objects, "OAuthClient", "bedrock-console").Object, "spec", "redirectURIs")
	if redirects[0] != "https://console.new.example/oauth/callback" {
		t.Fatalf("console redirect %v", redirects)
	}
	sslip, err := RenderAuthn(authnInput("", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	if authentication, _ := renderedAuthnFiles(t, sslip); !strings.Contains(authentication, "https://sso.10-0-0-250.sslip.io") {
		t.Fatalf("an empty platform.host falls back to the VIP host:\n%s", authentication)
	}
}

func TestRenderAuthnCustomCA(t *testing.T) {
	in := authnInput("lab.example", "Custom")
	in.PlatformCA = nil
	in.CustomCA = []byte("-----BEGIN CERTIFICATE-----\nCUSTOM\n-----END CERTIFICATE-----\n")
	out, err := RenderAuthn(in)
	if err != nil {
		t.Fatal(err)
	}
	authentication, webhook := renderedAuthnFiles(t, out)
	if !strings.Contains(authentication, "CUSTOM") || strings.Contains(authentication, "MIIB") {
		t.Fatalf("Custom mode trusts the custom CA:\n%s", authentication)
	}
	config, err := clientcmd.Load(webhook)
	if err != nil {
		t.Fatal(err)
	}
	if string(config.Clusters["bedrock-authn"].CertificateAuthorityData) != string(in.CustomCA) {
		t.Fatal("the webhook trusts the custom CA")
	}
	letsEncrypt, err := RenderAuthn(authnInput("lab.example", "LetsEncrypt"))
	if err != nil {
		t.Fatal(err)
	}
	if authentication, _ := renderedAuthnFiles(t, letsEncrypt); strings.Contains(authentication, "certificateAuthority") {
		t.Fatalf("LetsEncrypt uses the system roots:\n%s", authentication)
	}
	if _, err := RenderAuthn(authnInput("lab.example", "Plain")); err == nil {
		t.Fatal("an unknown TLS mode must fail")
	}
}

func TestRenderAuthnWaitsForTheToken(t *testing.T) {
	cases := map[string]struct {
		change func(AddonInput) AddonInput
		reason string
	}{
		"no deployment": {func(in AddonInput) AddonInput { in.AuthnInstalled = false; return in }, "AuthnNotInstalled"},
		"no token":      {func(in AddonInput) AddonInput { in.WebhookToken = ""; return in }, "WaitingForWebhookToken"},
		"no CA":         {func(in AddonInput) AddonInput { in.PlatformCA = nil; return in }, "WaitingForPlatformCA"},
	}
	for name, tc := range cases {
		out, err := RenderAuthn(tc.change(authnInput("lab.example", "SelfSigned")))
		if err != nil {
			t.Fatal(err)
		}
		if out.SkipReason != tc.reason || len(out.Objects) != 0 {
			t.Fatalf("%s: rendered %+v", name, out)
		}
	}
}

func TestAddonInputReadsAuthnSources(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	cluster := v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{API: v1alpha1.APISpec{VIP: "10.0.0.250"}}}
	r := &AddonReconciler{Client: c}
	in, err := r.input(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if in.AuthnInstalled || in.WebhookToken != "" || in.CustomCA != nil {
		t.Fatalf("nothing exists yet: %+v", in)
	}
	labels := map[string]string{"app": "bedrock-authn"}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: "bedrock-authn"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "authn", Image: "ghcr.io/cloudyfolks-io/bedrock:dev"}}}},
		},
	}
	objects := []client.Object{
		deployment,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: apiserver.TokenSecretName}, Data: map[string][]byte{"token": []byte("bearer-1")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: "my-tls"}, Data: map[string][]byte{"tls.crt": []byte("CRT"), "tls.key": []byte("KEY"), "ca.crt": []byte("CUSTOM")}},
		&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "platform.tls-mode"}, Spec: v1alpha1.SettingSpec{Value: "Custom"}},
		&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "platform.custom-tls"}, Spec: v1alpha1.SettingSpec{Value: "my-tls"}},
	}
	for _, obj := range objects {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	in, err = r.input(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if !in.AuthnInstalled || in.WebhookToken != "bearer-1" || string(in.CustomCA) != "CUSTOM" {
		t.Fatalf("input %+v", in)
	}
}
