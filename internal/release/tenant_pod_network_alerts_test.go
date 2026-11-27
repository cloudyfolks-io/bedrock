package release

import (
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func tenantPodNetworkAlerts(t *testing.T) map[string]map[string]any {
	t.Helper()
	rule := objectNamed(sourceObjects(t), "PrometheusRule", "bedrock-tenant-pod-network")
	if rule == nil {
		t.Fatal("missing PrometheusRule bedrock-tenant-pod-network")
	}
	groups, _, _ := unstructured.NestedSlice(rule.Object, "spec", "groups")
	alerts := map[string]map[string]any{}
	for _, group := range groups {
		for _, item := range group.(map[string]any)["rules"].([]any) {
			entry := item.(map[string]any)
			alerts[entry["alert"].(string)] = entry
		}
	}
	return alerts
}

func TestTenantPodNetworkAlertsCoverAuditDenyAndError(t *testing.T) {
	alerts := tenantPodNetworkAlerts(t)
	want := map[string]struct{ severity, fragment string }{
		"TenantPodNetworkAudit":  {"warning", `enforcement_action=~"audit|warn"`},
		"TenantPodNetworkDenied": {"critical", `enforcement_action="deny"`},
		"TenantPodNetworkError":  {"warning", `error_type=~"compile_error|out_of_budget"`},
	}
	if len(alerts) != len(want) {
		t.Fatalf("alerts %v", alerts)
	}
	for name, expected := range want {
		alert := alerts[name]
		if alert == nil {
			t.Fatalf("missing alert %s", name)
		}
		if labels := alert["labels"].(map[string]any); labels["severity"] != expected.severity {
			t.Errorf("%s severity %v", name, labels["severity"])
		}
		if expr := alert["expr"].(string); !strings.Contains(expr, expected.fragment) {
			t.Errorf("%s expr %q lacks %s", name, expr, expected.fragment)
		}
	}
}

func TestTenantPodNetworkErrorExcludesValidationFailures(t *testing.T) {
	expr := tenantPodNetworkAlerts(t)["TenantPodNetworkError"]["expr"].(string)
	_, selector, found := strings.Cut(expr, `error_type=~"`)
	if !found {
		t.Fatal("missing error type matcher")
	}
	pattern, _, found := strings.Cut(selector, `"`)
	if !found {
		t.Fatal("unterminated error type matcher")
	}
	matcher := regexp.MustCompile("^(?:" + pattern + ")$")
	for label, want := range map[string]bool{"compile_error": true, "out_of_budget": true, "invalid_error": false, "no_error": false, "": false} {
		if got := matcher.MatchString(label); got != want {
			t.Errorf("error type %q selected = %t, want %t", label, got, want)
		}
	}
}

func TestTenantPodNetworkAlertsSelectEveryPolicyOfTheFamily(t *testing.T) {
	for name, alert := range tenantPodNetworkAlerts(t) {
		if !strings.Contains(alert["expr"].(string), `policy=~"bedrock-tenant-pod-network.*"`) {
			t.Errorf("%s does not select the policy family", name)
		}
	}
	policies := 0
	for _, obj := range tenancyManifest(t, "85-tenant-pod-network-policy.yaml") {
		if obj.GetKind() == "ValidatingAdmissionPolicy" && strings.HasPrefix(obj.GetName(), "bedrock-tenant-pod-network") {
			policies++
		}
	}
	if policies != 3 {
		t.Fatalf("policies %d", policies)
	}
}
