package release

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const etcdQuotaBytes = 2147483648

func etcdAlertRule(t *testing.T) map[string]any {
	t.Helper()
	rule := objectNamed(sourceObjects(t), "PrometheusRule", "bedrock-etcd")
	if rule == nil {
		t.Fatal("missing PrometheusRule bedrock-etcd")
	}
	groups, _, _ := unstructured.NestedSlice(rule.Object, "spec", "groups")
	if len(groups) != 1 {
		t.Fatalf("groups %v", groups)
	}
	rules := groups[0].(map[string]any)["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("rules %v", rules)
	}
	return rules[0].(map[string]any)
}

func TestEtcdAlertFiresAboveSeventyPercentOfTheQuota(t *testing.T) {
	rule := etcdAlertRule(t)
	if rule["alert"] != "EtcdDatabaseNearQuota" {
		t.Fatalf("alert %v", rule["alert"])
	}
	expr := rule["expr"].(string)
	threshold := regexp.MustCompile(`> (\d+)$`).FindStringSubmatch(expr)
	if threshold == nil {
		t.Fatalf("expr %q has no literal threshold", expr)
	}
	want := "1503238554"
	if threshold[1] != want || int64(etcdQuotaBytes)*7/10+1 != 1503238554 {
		t.Errorf("threshold %s, want %s", threshold[1], want)
	}
	if !strings.HasPrefix(expr, "(") || !strings.Contains(expr, ") > ") {
		t.Errorf("expr %q must compare the union of both metrics", expr)
	}
	for _, metric := range []string{"etcd_mvcc_db_total_size_in_bytes", "apiserver_storage_size_bytes"} {
		if !strings.Contains(expr, metric) {
			t.Errorf("expr %q lacks %s", expr, metric)
		}
	}
}

func TestEtcdAlertHasASeverityAndAnAnnotation(t *testing.T) {
	rule := etcdAlertRule(t)
	if labels := rule["labels"].(map[string]any); labels["severity"] != "warning" {
		t.Errorf("labels %v", labels)
	}
	annotations := rule["annotations"].(map[string]any)
	for _, key := range []string{"summary", "description"} {
		if annotations[key] == "" || annotations[key] == nil {
			t.Errorf("missing annotation %s", key)
		}
	}
	if rule["for"] != "15m" {
		t.Errorf("for %v", rule["for"])
	}
}

func TestEtcdAlertFileHasNoComments(t *testing.T) {
	raw, err := os.ReadFile("../../" + monitoringSourceDir + "/25-etcd-alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?m)^\s*#`).Match(raw) {
		t.Fatal("comment line found")
	}
}

func TestEtcdAlertReachesTheBuiltStack(t *testing.T) {
	bundle, _ := buildStack(t)
	if objectNamed(groupObjects(bundle, "kube-prometheus-stack"), "PrometheusRule", "bedrock-etcd") == nil {
		t.Fatal("bedrock-etcd is not in the built group")
	}
}
