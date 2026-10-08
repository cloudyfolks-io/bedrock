package release

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const monitoringSourceDir = "manifests/71-kube-prometheus-stack"

func sourceObjects(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	objects, err := loadObjects(os.DirFS("../.."), monitoringSourceDir, "kube-prometheus-stack")
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

type meteringRule struct{ record, expr string }

func meteringRules(t *testing.T) []meteringRule {
	t.Helper()
	rule := objectNamed(sourceObjects(t), "PrometheusRule", "bedrock-metering")
	if rule == nil {
		t.Fatal("missing PrometheusRule bedrock-metering")
	}
	groups, _, _ := unstructured.NestedSlice(rule.Object, "spec", "groups")
	if len(groups) != 1 {
		t.Fatalf("groups %v", groups)
	}
	group := groups[0].(map[string]any)
	if group["name"] != "bedrock.metering" || group["interval"] != "1m" {
		t.Fatalf("group %v", group)
	}
	var rules []meteringRule
	for _, item := range group["rules"].([]any) {
		entry := item.(map[string]any)
		rules = append(rules, meteringRule{record: entry["record"].(string), expr: entry["expr"].(string)})
	}
	return rules
}

var meteringSeries = []string{
	"bedrock:project_vm_running:count",
	"bedrock:project_vm_vcpu:sum",
	"bedrock:project_vm_memory_bytes:sum",
	"bedrock:project_volume_requested_bytes:sum",
	"bedrock:project_vm_cpu_usage_cores:rate5m",
	"bedrock:project_vm_network_receive_bytes:rate5m",
	"bedrock:project_vm_network_transmit_bytes:rate5m",
}

var sourceMetrics = []string{
	"kubevirt_vmi_info",
	"kubevirt_vmi_vcpu_seconds_total",
	"kubevirt_vmi_memory_domain_bytes",
	"kube_persistentvolumeclaim_resource_requests_storage_bytes",
	"kubevirt_vmi_cpu_usage_seconds_total",
	"kubevirt_vmi_network_receive_bytes_total",
	"kubevirt_vmi_network_transmit_bytes_total",
	"kube_namespace_labels",
}

var metricIdentifier = regexp.MustCompile(`\b(?:kubevirt_|kube_)[a-z0-9_]+`)

func TestMeteringRulesNameEverySeries(t *testing.T) {
	var got []string
	for _, rule := range meteringRules(t) {
		got = append(got, rule.record)
	}
	want := slices.Clone(meteringSeries)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("series %v", got)
	}
}

func TestMeteringRulesAreProjectScoped(t *testing.T) {
	for _, rule := range meteringRules(t) {
		for _, part := range []string{`kube_namespace_labels{label_bedrock_cloudyfolks_io_project!=""}`, "group_left (tenant, project)"} {
			if !strings.Contains(rule.expr, part) {
				t.Errorf("%s lacks %s", rule.record, part)
			}
		}
		if !strings.HasSuffix(rule.expr, "by (tenant, project)") {
			t.Errorf("%s does not end in a by (tenant, project) aggregation", rule.record)
		}
	}
}

func TestMeteringRulesDoNotMatchNamespaceNames(t *testing.T) {
	for _, rule := range meteringRules(t) {
		for _, forbidden := range []string{"namespace=~", "namespace=", "namespace!=", "t-["} {
			if strings.Contains(rule.expr, forbidden) {
				t.Errorf("%s contains %s", rule.record, forbidden)
			}
		}
	}
}

func TestMeteringRulesUseKnownSourceMetrics(t *testing.T) {
	for _, rule := range meteringRules(t) {
		for _, metric := range metricIdentifier.FindAllString(rule.expr, -1) {
			if !slices.Contains(sourceMetrics, metric) {
				t.Errorf("%s uses %s", rule.record, metric)
			}
		}
	}
}

func TestMeteringRulesHaveNoComments(t *testing.T) {
	raw, err := os.ReadFile("../../" + monitoringSourceDir + "/10-bedrock-metering.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?m)^\s*#`).Match(raw) {
		t.Fatal("comment line found")
	}
}

func TestMeteringRulesReachTheBuiltStack(t *testing.T) {
	bundle, _ := buildStack(t)
	if objectNamed(groupObjects(bundle, "kube-prometheus-stack"), "PrometheusRule", "bedrock-metering") == nil {
		t.Fatal("bedrock-metering is not in the built group")
	}
	if _, err := json.Marshal(bundle.Groups); err != nil {
		t.Fatal(err)
	}
}
