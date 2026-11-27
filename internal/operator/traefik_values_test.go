package operator

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/registry"
)

func TestTraefikAllowsLongUploads(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "release", "values", "traefik.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Ports struct {
			Websecure struct {
				Transport struct {
					RespondingTimeouts struct {
						ReadTimeout string `json:"readTimeout"`
					} `json:"respondingTimeouts"`
				} `json:"transport"`
			} `json:"websecure"`
		} `json:"ports"`
		Service struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"service"`
		TLSStore struct {
			Default struct {
				DefaultCertificate struct {
					SecretName string `json:"secretName"`
				} `json:"defaultCertificate"`
			} `json:"default"`
		} `json:"tlsStore"`
	}
	if err := sigyaml.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	if got := values.Ports.Websecure.Transport.RespondingTimeouts.ReadTimeout; got != "2h" {
		t.Fatalf("readTimeout %q", got)
	}
	if values.Service.Annotations["kube-vip.io/loadbalancerIPs"] != "${BEDROCK_VIP}" {
		t.Fatalf("service annotations %v", values.Service.Annotations)
	}
	if values.TLSStore.Default.DefaultCertificate.SecretName != "platform-tls" {
		t.Fatalf("tls store %+v", values.TLSStore)
	}
}

func TestTraefikReadsOnlyTheNamespacesOfBedrockRoutes(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "release", "values", "traefik.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Providers struct {
			KubernetesCRD     struct{ Namespaces []string } `json:"kubernetesCRD"`
			KubernetesIngress struct{ Namespaces []string } `json:"kubernetesIngress"`
		} `json:"providers"`
	}
	if err := sigyaml.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	routed := []string{
		traefikNamespace,
		authnIngressRoute("example.com").GetNamespace(),
		proxyIngressRoute("example.com").GetNamespace(),
		uploadIngressRoute("example.com").GetNamespace(),
		objectstorage.StoreNamespace,
		registry.Namespace,
	}
	for name, got := range map[string][]string{"kubernetesCRD": values.Providers.KubernetesCRD.Namespaces, "kubernetesIngress": values.Providers.KubernetesIngress.Namespaces} {
		for _, namespace := range routed {
			if !slices.Contains(got, namespace) {
				t.Errorf("%s misses the namespace %s", name, namespace)
			}
		}
		if len(got) != len(slices.Compact(slices.Sorted(slices.Values(routed)))) {
			t.Errorf("%s watches %v, want only %v", name, got, routed)
		}
	}
}
