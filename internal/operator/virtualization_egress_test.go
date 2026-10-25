package operator

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func cdiImportProxy(t *testing.T, settings map[string]string) (map[string]any, bool, error) {
	t.Helper()
	out, err := RenderVirtualization(AddonInput{Cluster: testCluster(), Settings: settings})
	if err != nil {
		return nil, false, err
	}
	cdi := findObject(out.Objects, "CDI", "cdi")
	proxy, found, _ := unstructured.NestedMap(cdi.Object, "spec", "config", "importProxy")
	return proxy, found, nil
}

func TestCDIHasNoImportProxyByDefault(t *testing.T) {
	_, found, err := cdiImportProxy(t, map[string]string{})
	if err != nil || found {
		t.Fatalf("importProxy present %v, error %v", found, err)
	}
}

func TestCDIImportProxyFollowsTheEgressSettings(t *testing.T) {
	proxy, found, err := cdiImportProxy(t, map[string]string{
		"egress.http-proxy": "http://proxy.kube-system:10808",
		"egress.no-proxy":   "10.0.0.0/8,.cloudyfolks.com",
	})
	if err != nil || !found {
		t.Fatalf("importProxy found %v, error %v", found, err)
	}
	want := map[string]any{"HTTPProxy": "http://proxy.kube-system:10808", "HTTPSProxy": "http://proxy.kube-system:10808", "noProxy": derivedNoProxy + ",10.0.0.0/8,.cloudyfolks.com"}
	if len(proxy) != len(want) {
		t.Fatalf("importProxy %v", proxy)
	}
	for key, value := range want {
		if proxy[key] != value {
			t.Fatalf("importProxy %v", proxy)
		}
	}
}

func TestCDIImportProxyKeepsClusterTrafficOffTheProxyWithAnEmptySetting(t *testing.T) {
	proxy, _, err := cdiImportProxy(t, map[string]string{"egress.http-proxy": "http://proxy.kube-system:10808"})
	if err != nil {
		t.Fatal(err)
	}
	if proxy["noProxy"] != derivedNoProxy {
		t.Fatalf("importProxy %v", proxy)
	}
}

func TestCDIIgnoresNoProxyWithoutAProxy(t *testing.T) {
	_, found, err := cdiImportProxy(t, map[string]string{"egress.no-proxy": ".svc"})
	if err != nil || found {
		t.Fatalf("importProxy present %v, error %v", found, err)
	}
}

func TestRenderVirtualizationRejectsAnInvalidProxy(t *testing.T) {
	if _, _, err := cdiImportProxy(t, map[string]string{"egress.http-proxy": "proxy:3128"}); err == nil || !strings.Contains(err.Error(), "egress.http-proxy") {
		t.Fatalf("error %v", err)
	}
}
