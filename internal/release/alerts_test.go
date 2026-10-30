package release

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const runbookBase = "https://github.com/cloudyfolks-io/bedrock/blob/main/docs/alerts.md#"

var alertRuleSources = []string{
	"manifests/71-kube-prometheus-stack",
	"manifests/addons/loki",
	"manifests/addons/cnpg",
}

func ruleObjects(t *testing.T, dirs ...string) []*unstructured.Unstructured {
	t.Helper()
	var rules []*unstructured.Unstructured
	for _, dir := range dirs {
		objects, err := loadObjects(os.DirFS("../.."), dir, "alerts")
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range objects {
			if object.GetKind() == "PrometheusRule" {
				rules = append(rules, object)
			}
		}
	}
	return rules
}

func alertsOf(t *testing.T, object *unstructured.Unstructured) []map[string]any {
	t.Helper()
	groups, _, _ := unstructured.NestedSlice(object.Object, "spec", "groups")
	var alerts []map[string]any
	for _, group := range groups {
		for _, item := range group.(map[string]any)["rules"].([]any) {
			if rule := item.(map[string]any); rule["alert"] != nil {
				alerts = append(alerts, rule)
			}
		}
	}
	return alerts
}

func alertsDocAnchors(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../docs/alerts.md")
	if err != nil {
		t.Fatal(err)
	}
	var anchors []string
	for _, line := range strings.Split(string(raw), "\n") {
		if name, ok := strings.CutPrefix(line, "### "); ok {
			anchors = append(anchors, strings.ToLower(strings.TrimSpace(name)))
		}
	}
	return anchors
}

func TestEveryBedrockAlertHasARunbookSectionAndTheRequiredFields(t *testing.T) {
	anchors := alertsDocAnchors(t)
	seen := 0
	for _, object := range ruleObjects(t, alertRuleSources...) {
		for _, rule := range alertsOf(t, object) {
			seen++
			name := rule["alert"].(string)
			for _, key := range []string{"expr"} {
				if rule[key] == nil || rule[key] == "" {
					t.Errorf("%s: missing %s", name, key)
				}
			}
			labels, _ := rule["labels"].(map[string]any)
			if labels["severity"] != "warning" && labels["severity"] != "critical" {
				t.Errorf("%s: severity %v", name, labels["severity"])
			}
			annotations, _ := rule["annotations"].(map[string]any)
			for _, key := range []string{"summary", "description"} {
				if annotations[key] == nil || annotations[key] == "" {
					t.Errorf("%s: missing %s", name, key)
				}
			}
			url, _ := annotations["runbook_url"].(string)
			anchor, found := strings.CutPrefix(url, runbookBase)
			if !found || anchor != strings.ToLower(name) || !slices.Contains(anchors, anchor) {
				t.Errorf("%s: runbook_url %q has no section in docs/alerts.md", name, url)
			}
		}
	}
	if seen < 40 {
		t.Fatalf("only %d alerts found", seen)
	}
}

func TestBedrockAlertsCoverTheBlockerList(t *testing.T) {
	var names []string
	for _, object := range ruleObjects(t, alertRuleSources...) {
		for _, rule := range alertsOf(t, object) {
			names = append(names, rule["alert"].(string))
		}
	}
	for _, want := range []string{
		"CephOSDNearFull", "CephOSDFull", "CephOSDUsageAboveOneLossLimit", "CephPoolNearQuota", "CephPGsNotClean", "CephPGsNotActive", "CephOSDDown", "CephMonQuorumLost", "CephSlowOps",
		"BedrockUpgradeStuck", "BedrockUpgradeFailed", "BedrockNodeUpgradeStepFailed", "BedrockOperatorNoLeader", "BedrockTenantLegsUnguarded", "BedrockNodePortsUnscoped", "BedrockEncryptionRotationStalled",
		"BedrockEtcdSnapshotStale", "BedrockEtcdSnapshotFailing", "CertManagerCertificateExpiringSoon",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("missing alert %s", want)
		}
	}
}

func TestAlertFilesHaveNoComments(t *testing.T) {
	files, err := filepath.Glob("../../" + monitoringSourceDir + "/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	comment := regexp.MustCompile(`(?m)^\s*#`)
	for _, file := range files {
		raw, _ := os.ReadFile(file)
		if comment.Match(raw) {
			t.Errorf("%s has a comment", file)
		}
	}
}

func TestRulesExpressionsHaveBalancedBrackets(t *testing.T) {
	for _, object := range ruleObjects(t, alertRuleSources...) {
		for _, rule := range alertsOf(t, object) {
			expr := rule["expr"].(string)
			for _, pair := range []string{"()", "[]", "{}"} {
				if strings.Count(expr, pair[:1]) != strings.Count(expr, pair[1:]) {
					t.Errorf("%s: unbalanced %s in %q", rule["alert"], pair, expr)
				}
			}
		}
	}
}

func TestStackShipsTheScrapesTheAlertsNeed(t *testing.T) {
	bundle, _ := buildStack(t)
	objects := groupObjects(bundle, "kube-prometheus-stack")
	for kind, names := range map[string][]string{
		"ServiceMonitor": {"bedrock-fabric-pinger", "bedrock-fabric-frr"},
		"PodMonitor":     {"bedrock-operator"},
		"PrometheusRule": {"bedrock-ceph", "bedrock-operator", "bedrock-certificates", "bedrock-backup", "bedrock-etcd"},
	} {
		for _, name := range names {
			if objectNamed(objects, kind, name) == nil {
				t.Errorf("missing %s %s", kind, name)
			}
		}
	}
	nodeExporter := objectNamed(objects, "DaemonSet", "kube-prometheus-stack-prometheus-node-exporter")
	if nodeExporter == nil {
		t.Fatal("missing node exporter")
	}
	raw, _ := yaml.Marshal(nodeExporter.Object)
	for _, want := range []string{"--collector.textfile.directory=/host/textfile", "/var/lib/node_exporter/textfile", "--collector.filesystem.mount-points-exclude"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("node exporter lacks %s", want)
		}
	}
}

func TestFabricRendersItsMonitorsAndRulesWhenMonitoringIsOn(t *testing.T) {
	bundle, _ := buildReal(t, "fabric")
	objects := groupObjects(bundle, "fabric")
	for _, name := range []string{"fabric-controller", "fabric-monitor", "fabric-cni"} {
		if objectNamed(objects, "ServiceMonitor", name) == nil {
			t.Errorf("missing ServiceMonitor %s", name)
		}
	}
	rule := objectNamed(objects, "PrometheusRule", "fabric")
	if rule == nil {
		t.Fatal("missing PrometheusRule fabric")
	}
	anchors := alertsDocAnchors(t)
	for _, alert := range alertsOf(t, rule) {
		url, _ := alert["annotations"].(map[string]any)["runbook_url"].(string)
		if !slices.Contains(anchors, strings.TrimPrefix(url, runbookBase)) {
			t.Errorf("%s: runbook_url %q", alert["alert"], url)
		}
	}
}

func TestFabricRendersNoMonitorsWhenMonitoringIsOff(t *testing.T) {
	raw, err := os.ReadFile("../../release/values/fabric.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	delete(values, "prometheusRule")
	for _, key := range []string{"controller", "monitor", "agent"} {
		delete(values, key)
	}
	off, err := yaml.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "fabric-monitoring-off.yaml")
	if err := os.WriteFile(path, off, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
	bundle, _ := buildRealWithValues(t, "fabric", "internal/release/"+path)
	for _, object := range groupObjects(bundle, "fabric") {
		if object.GetKind() == "ServiceMonitor" || object.GetKind() == "PrometheusRule" {
			t.Errorf("unexpected %s %s", object.GetKind(), object.GetName())
		}
	}
}

func TestRookAndCertManagerValuesTurnOnMonitoring(t *testing.T) {
	for file, path := range map[string][]string{
		"rook.yaml":         {"monitoring", "enabled"},
		"cert-manager.yaml": {"prometheus", "servicemonitor", "enabled"},
	} {
		raw, err := os.ReadFile("../../release/values/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]any
		if err := yaml.Unmarshal(raw, &values); err != nil {
			t.Fatal(err)
		}
		on, _, _ := unstructured.NestedBool(values, path...)
		if !on {
			t.Errorf("%s: %v is not true", file, path)
		}
	}
}
