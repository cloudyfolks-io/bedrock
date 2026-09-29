package apiserver

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	sigyaml "sigs.k8s.io/yaml"
)

const testCA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func parseYAML(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := sigyaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func expectedAuthentication(issuer map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "apiserver.config.k8s.io/v1",
		"kind":       "AuthenticationConfiguration",
		"jwt": []any{map[string]any{
			"issuer": issuer,
			"claimMappings": map[string]any{
				"username": map[string]any{"claim": "sub", "prefix": "bedrock:"},
				"groups":   map[string]any{"claim": "groups", "prefix": "bedrock:"},
			},
		}},
	}
}

func TestAuthenticationConfigSelfSigned(t *testing.T) {
	raw, err := AuthenticationConfig(Inputs{Host: "lab.example", CA: []byte(testCA), Bearer: "secret-bearer"})
	if err != nil {
		t.Fatal(err)
	}
	want := expectedAuthentication(map[string]any{"url": "https://sso.lab.example", "audiences": []any{"bedrock"}, "certificateAuthority": testCA})
	if got := parseYAML(t, raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("authentication.yaml\n%s", raw)
	}
	if strings.Contains(string(raw), "secret-bearer") {
		t.Fatal("the bearer belongs only to the webhook kubeconfig")
	}
}

func TestAuthenticationConfigLetsEncryptHasNoCA(t *testing.T) {
	raw, err := AuthenticationConfig(Inputs{Host: "cloud.example.com", Bearer: "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := expectedAuthentication(map[string]any{"url": "https://sso.cloud.example.com", "audiences": []any{"bedrock"}})
	if got := parseYAML(t, raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("authentication.yaml\n%s", raw)
	}
}

func TestWebhookKubeconfig(t *testing.T) {
	raw, err := WebhookKubeconfig(Inputs{Host: "lab.example", CA: []byte(testCA), Bearer: "secret-bearer"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	current := config.Contexts[config.CurrentContext]
	if config.CurrentContext != "bedrock-authn" || current == nil || current.Cluster != "bedrock-authn" || current.AuthInfo != "kube-apiserver" {
		t.Fatalf("context %q %+v", config.CurrentContext, current)
	}
	cluster := config.Clusters["bedrock-authn"]
	if cluster == nil || cluster.Server != "https://sso.lab.example/webhook/v1/tokenreview" || string(cluster.CertificateAuthorityData) != testCA {
		t.Fatalf("cluster %+v", cluster)
	}
	if user := config.AuthInfos["kube-apiserver"]; user == nil || user.Token != "secret-bearer" {
		t.Fatalf("user %+v", user)
	}
	raw, err = WebhookKubeconfig(Inputs{Host: "cloud.example.com", Bearer: "b"})
	if err != nil {
		t.Fatal(err)
	}
	config, err = clientcmd.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cluster := config.Clusters["bedrock-authn"]; len(cluster.CertificateAuthorityData) != 0 || cluster.Server != "https://sso.cloud.example.com/webhook/v1/tokenreview" {
		t.Fatalf("LetsEncrypt uses the system roots: %+v", cluster)
	}
}

func TestHashChangesWithEitherFile(t *testing.T) {
	base := Hash([]byte("a"), []byte("b"))
	if !strings.HasPrefix(base, "sha256:") || len(base) != len("sha256:")+64 {
		t.Fatalf("hash %q", base)
	}
	if base != Hash([]byte("a"), []byte("b")) {
		t.Fatal("the hash must be stable")
	}
	for _, other := range []string{Hash([]byte("x"), []byte("b")), Hash([]byte("a"), []byte("x")), Hash([]byte("ab"), nil), Hash(nil, []byte("ab"))} {
		if other == base {
			t.Fatalf("hash %q must differ", other)
		}
	}
	if Hash([]byte("ab"), nil) == Hash([]byte("a"), []byte("b")) {
		t.Fatal("the separator must keep the files apart")
	}
}

func TestArgs(t *testing.T) {
	want := map[string]string{
		"authentication-config":                    "/etc/bedrock/authn/authentication.yaml",
		"authentication-token-webhook-config-file": "/etc/bedrock/authn/webhook.kubeconfig",
		"authentication-token-webhook-cache-ttl":   "30s",
		"authentication-token-webhook-version":     "v1",
	}
	if got := Args(); !reflect.DeepEqual(got, want) {
		t.Fatalf("args %v", got)
	}
}

func TestKubeAPIServerAcceptsTheConfig(t *testing.T) {
	ca, _, err := NewCA(rand.Reader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	in := Inputs{Host: "lab.example", CA: ca, Bearer: "bearer"}
	authentication, err := AuthenticationConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	webhook, err := WebhookKubeconfig(in)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string][]byte{AuthenticationFile: authentication, WebhookFile: webhook} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := &envtest.Environment{}
	flags := env.ControlPlane.GetAPIServer().Configure()
	for name, value := range Args() {
		flags.Set(name, value)
	}
	flags.Set("authentication-config", filepath.Join(dir, AuthenticationFile))
	flags.Set("authentication-token-webhook-config-file", filepath.Join(dir, WebhookFile))
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("kube-apiserver refused the files: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
}
