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
	"bedrock:project_vm_network_receive_bytes:increase1h",
	"bedrock:project_vm_network_transmit_bytes:increase1h",
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

func TestMeteringHourlyNetworkRulesSumTheIncreaseOfEachSeries(t *testing.T) {
	want := map[string]string{
		"bedrock:project_vm_network_receive_bytes:increase1h":  "sum (increase(kubevirt_vmi_network_receive_bytes_total[1h]) * on (namespace)",
		"bedrock:project_vm_network_transmit_bytes:increase1h": "sum (increase(kubevirt_vmi_network_transmit_bytes_total[1h]) * on (namespace)",
	}
	seen := 0
	for _, rule := range meteringRules(t) {
		prefix, ok := want[rule.record]
		if !ok {
			continue
		}
		seen++
		if !strings.HasPrefix(rule.expr, prefix) {
			t.Errorf("%s does not take the increase before the sum: %s", rule.record, rule.expr)
		}
	}
	if seen != len(want) {
		t.Fatalf("found %d of %d hourly network rules", seen, len(want))
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

func dashboardPanels(t *testing.T, name string) (map[string]any, []any) {
	t.Helper()
	config := objectNamed(sourceObjects(t), "ConfigMap", name)
	if config == nil {
		t.Fatalf("missing ConfigMap %s", name)
	}
	if config.GetLabels()["grafana_dashboard"] != "1" || config.GetNamespace() != "monitoring" {
		t.Fatalf("%s labels %v", name, config.GetLabels())
	}
	data, _, _ := unstructured.NestedStringMap(config.Object, "data")
	if len(data) != 1 {
		t.Fatalf("%s data keys %d", name, len(data))
	}
	for _, raw := range data {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return parsed, parsed["panels"].([]any)
	}
	return nil, nil
}

func dashboardExprs(t *testing.T) []string {
	t.Helper()
	var exprs []string
	for _, name := range []string{"bedrock-dashboard-vms", "bedrock-dashboard-tenants"} {
		parsed, panels := dashboardPanels(t, name)
		for _, item := range panels {
			for _, target := range item.(map[string]any)["targets"].([]any) {
				exprs = append(exprs, target.(map[string]any)["expr"].(string))
			}
		}
		templating, _ := parsed["templating"].(map[string]any)
		variables, _ := templating["list"].([]any)
		for _, item := range variables {
			exprs = append(exprs, item.(map[string]any)["query"].(map[string]any)["query"].(string))
		}
	}
	return exprs
}

func TestDashboardsAreValidAndLabelled(t *testing.T) {
	want := map[string]string{"bedrock-dashboard-vms": "bedrock-vms", "bedrock-dashboard-tenants": "bedrock-tenants"}
	seen := map[string]bool{}
	for name, uid := range want {
		parsed, panels := dashboardPanels(t, name)
		if parsed["uid"] != uid || seen[uid] {
			t.Errorf("%s uid %v", name, parsed["uid"])
		}
		seen[uid] = true
		if title, _ := parsed["title"].(string); !strings.HasPrefix(title, "Bedrock / ") {
			t.Errorf("%s title %q", name, title)
		}
		if parsed["schemaVersion"].(float64) < 39 || parsed["refresh"] != "1m" || parsed["time"].(map[string]any)["from"] != "now-6h" {
			t.Errorf("%s time settings", name)
		}
		for _, item := range panels {
			panel := item.(map[string]any)
			if panel["datasource"].(map[string]any)["uid"] != "prometheus" {
				t.Errorf("%s panel %v datasource", name, panel["title"])
			}
			for _, target := range panel["targets"].([]any) {
				entry := target.(map[string]any)
				if expr, _ := entry["expr"].(string); expr == "" {
					t.Errorf("%s panel %v has an empty expr", name, panel["title"])
				}
				if entry["datasource"].(map[string]any)["uid"] != "prometheus" {
					t.Errorf("%s panel %v target datasource", name, panel["title"])
				}
			}
		}
	}
}

func TestDashboardsUseKnownMetrics(t *testing.T) {
	allowed := append(slices.Clone(sourceMetrics), meteringSeries...)
	allowed = append(allowed,
		"kubevirt_vmi_vcpu_delay_seconds_total",
		"kubevirt_vmi_memory_resident_bytes",
		"kubevirt_vmi_storage_read_traffic_bytes_total",
		"kubevirt_vmi_storage_write_traffic_bytes_total",
		"kube_namespace_status_phase",
	)
	identifier := regexp.MustCompile(`\b(?:kubevirt_|kube_)[a-z0-9_]+|\bbedrock:[a-z0-9_:]+`)
	for _, expr := range dashboardExprs(t) {
		for _, metric := range identifier.FindAllString(expr, -1) {
			if !slices.Contains(allowed, metric) {
				t.Errorf("unknown metric %s in %s", metric, expr)
			}
		}
	}
}

func TestDashboardsSelectProjectsThroughTheNamespaceLabels(t *testing.T) {
	for _, expr := range dashboardExprs(t) {
		if strings.Contains(expr, "namespace=") || strings.Contains(expr, "namespace=~") {
			t.Errorf("namespace name matcher in %s", expr)
		}
	}
}

func TestKubeVirtServiceMonitorScrapesTheMetricsService(t *testing.T) {
	monitor := objectNamed(sourceObjects(t), "ServiceMonitor", "bedrock-kubevirt")
	if monitor == nil {
		t.Fatal("missing ServiceMonitor bedrock-kubevirt")
	}
	selector, _, _ := unstructured.NestedStringMap(monitor.Object, "spec", "selector", "matchLabels")
	namespaces, _, _ := unstructured.NestedStringSlice(monitor.Object, "spec", "namespaceSelector", "matchNames")
	endpoints, _, _ := unstructured.NestedSlice(monitor.Object, "spec", "endpoints")
	if selector["prometheus.kubevirt.io"] != "true" || !slices.Equal(namespaces, []string{"kubevirt"}) || len(endpoints) != 1 {
		t.Fatalf("selector %v namespaces %v endpoints %v", selector, namespaces, endpoints)
	}
	endpoint := endpoints[0].(map[string]any)
	if endpoint["port"] != "metrics" || endpoint["scheme"] != "https" || endpoint["honorLabels"] != true {
		t.Fatalf("endpoint %v", endpoint)
	}
}
