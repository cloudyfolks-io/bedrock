package release

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func groupObjects(bundle Bundle, name string) []*unstructured.Unstructured {
	for _, group := range bundle.Groups {
		if group.Name == name {
			return group.Objects
		}
	}
	return nil
}

func buildStack(t *testing.T) (Bundle, string) {
	t.Helper()
	return buildReal(t, "kube-prometheus-stack")
}

func TestPrometheusStackRendersTheOperatorAndTheCRs(t *testing.T) {
	bundle, _ := buildStack(t)
	objects := groupObjects(bundle, "kube-prometheus-stack")
	for _, name := range []string{"kube-prometheus-stack-operator", "kube-prometheus-stack-kube-state-metrics", "kube-prometheus-stack-grafana"} {
		if objectNamed(objects, "Deployment", name) == nil {
			t.Errorf("missing Deployment %s", name)
		}
	}
	if objectNamed(objects, "DaemonSet", "kube-prometheus-stack-prometheus-node-exporter") == nil {
		t.Error("missing node-exporter")
	}
	prometheus := objectNamed(objects, "Prometheus", "kube-prometheus-stack-prometheus")
	if prometheus == nil {
		t.Fatal("missing Prometheus")
	}
	if retention, _, _ := unstructured.NestedString(prometheus.Object, "spec", "retention"); retention != "15d" {
		t.Errorf("retention %q", retention)
	}
	claim, _, _ := unstructured.NestedMap(prometheus.Object, "spec", "storage", "volumeClaimTemplate", "spec")
	if claim["storageClassName"] != "block" || claim["resources"].(map[string]any)["requests"].(map[string]any)["storage"] != "50Gi" {
		t.Errorf("claim %v", claim)
	}
	selector, found, _ := unstructured.NestedMap(prometheus.Object, "spec", "ruleSelector")
	if !found || len(selector) != 0 {
		t.Errorf("ruleSelector %v found %v", selector, found)
	}
	alertmanager := objectNamed(objects, "Alertmanager", "kube-prometheus-stack-alertmanager")
	if alertmanager == nil {
		t.Fatal("missing Alertmanager")
	}
	if secret, _, _ := unstructured.NestedString(alertmanager.Object, "spec", "configSecret"); secret != "bedrock-alertmanager-config" {
		t.Errorf("configSecret %q", secret)
	}
}

func TestPrometheusStackMovesCRDsToTheCRDGroup(t *testing.T) {
	bundle, _ := buildStack(t)
	count := 0
	for _, obj := range groupObjects(bundle, "crds") {
		if obj.GetKind() == "CustomResourceDefinition" && strings.HasSuffix(obj.GetName(), ".monitoring.coreos.com") {
			count++
		}
	}
	if count != 10 {
		t.Errorf("crds in group crds: %d", count)
	}
	for _, obj := range groupObjects(bundle, "kube-prometheus-stack") {
		if obj.GetKind() == "CustomResourceDefinition" {
			t.Errorf("CRD %s left in the stack group", obj.GetName())
		}
	}
}

func TestPrometheusStackHasNoHooksOrTestPods(t *testing.T) {
	bundle, _ := buildStack(t)
	for _, obj := range groupObjects(bundle, "kube-prometheus-stack") {
		for key := range obj.GetAnnotations() {
			if strings.HasPrefix(key, "helm.sh/hook") {
				t.Errorf("%s %s has %s", obj.GetKind(), obj.GetName(), key)
			}
		}
	}
}

func TestPrometheusStackHoldsNoSecretMaterial(t *testing.T) {
	bundle, _ := buildStack(t)
	objects := groupObjects(bundle, "kube-prometheus-stack")
	for _, obj := range objects {
		if obj.GetKind() != "Secret" {
			continue
		}
		if obj.GetName() == "alertmanager-kube-prometheus-stack-alertmanager" {
			t.Errorf("secret %s", obj.GetName())
		}
		if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "data", "admin-password"); found {
			t.Errorf("secret %s holds admin-password", obj.GetName())
		}
		if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "stringData", "admin-password"); found {
			t.Errorf("secret %s holds admin-password", obj.GetName())
		}
	}
}

func TestGrafanaReadsTheBedrockAdminSecret(t *testing.T) {
	bundle, _ := buildStack(t)
	grafana := objectNamed(groupObjects(bundle, "kube-prometheus-stack"), "Deployment", "kube-prometheus-stack-grafana")
	if grafana == nil {
		t.Fatal("missing Grafana")
	}
	raw, err := grafana.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"name":"bedrock-grafana-admin"`) {
		t.Fatal("Grafana does not read bedrock-grafana-admin")
	}
}

func TestPrometheusStackSkipsControlPlaneRules(t *testing.T) {
	bundle, _ := buildStack(t)
	for _, obj := range groupObjects(bundle, "kube-prometheus-stack") {
		if obj.GetKind() != "PrometheusRule" {
			continue
		}
		for _, word := range []string{"etcd", "scheduler", "controller-manager", "kube-proxy"} {
			if strings.Contains(obj.GetName(), word) {
				t.Errorf("rule %s", obj.GetName())
			}
		}
	}
}

func TestKubeStateMetricsExportsTheProjectLabels(t *testing.T) {
	bundle, _ := buildStack(t)
	deployment := objectNamed(groupObjects(bundle, "kube-prometheus-stack"), "Deployment", "kube-prometheus-stack-kube-state-metrics")
	if deployment == nil {
		t.Fatal("missing kube-state-metrics")
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	args, _ := containers[0].(map[string]any)["args"].([]any)
	if !slices.Contains(args, any("--metric-labels-allowlist=namespaces=[bedrock.cloudyfolks.io/tenant,bedrock.cloudyfolks.io/project]")) {
		t.Fatalf("args %v", args)
	}
}

func TestPrometheusStackListsTheConfigReloader(t *testing.T) {
	_, out := buildStack(t)
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), "quay.io/prometheus-operator/prometheus-config-reloader:v0.94.1") {
		t.Fatalf("images %s", raw)
	}
}
