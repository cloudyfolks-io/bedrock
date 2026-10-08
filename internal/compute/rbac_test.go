package compute

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

func allowed(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	for _, rule := range rules {
		if contains(rule.APIGroups, group) && contains(rule.Resources, resource) && contains(rule.Verbs, verb) {
			return true
		}
	}
	return false
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestOperatorRules(t *testing.T) {
	rules := OperatorRules()
	for _, want := range []struct{ group, resource, verb string }{
		{"kubevirt.io", "virtualmachines", "patch"}, {"kubevirt.io", "virtualmachineinstances", "watch"},
		{"kubevirt.io", "virtualmachineinstancemigrations", "list"}, {"snapshot.kubevirt.io", "virtualmachinesnapshots", "delete"},
		{"instancetype.kubevirt.io", "virtualmachineclusterpreferences", "get"}, {"subresources.kubevirt.io", "virtualmachineinstances/guestosinfo", "get"},
		{"cdi.kubevirt.io", "datavolumes", "create"}, {"cdi.kubevirt.io", "datasources", "list"},
		{"", "persistentvolumeclaims", "patch"}, {"", "secrets", "update"}, {"", "pods", "patch"},
		{"snapshot.storage.k8s.io", "volumesnapshots", "create"}, {"snapshot.storage.k8s.io", "volumesnapshotclasses", "watch"},
		{"storage.k8s.io", "storageclasses", "get"}, {"bedrock.cloudyfolks.io", "volumes/finalizers", "update"},
	} {
		if !allowed(rules, want.group, want.resource, want.verb) {
			t.Errorf("missing %s %s/%s", want.verb, want.group, want.resource)
		}
	}
	for _, rule := range rules {
		if contains(rule.Resources, "datavolumes/source") || contains(rule.Resources, "*") || contains(rule.Verbs, "*") || contains(rule.APIGroups, "*") {
			t.Errorf("forbidden rule %+v", rule)
		}
	}
	if allowed(rules, "", "pods", "delete") || allowed(rules, "", "persistentvolumeclaims", "delete") {
		t.Error("the operator must not delete pods or claims")
	}
}

func TestProxyRules(t *testing.T) {
	rules := ProxyRules()
	for _, want := range []struct{ group, resource, verb string }{
		{"bedrock.cloudyfolks.io", "virtualmachines", "get"}, {"bedrock.cloudyfolks.io", "virtualmachines", "patch"},
		{"", "namespaces", "get"}, {"kubevirt.io", "virtualmachineinstancemigrations", "create"},
		{"snapshot.kubevirt.io", "virtualmachinesnapshots", "create"}, {"subresources.kubevirt.io", "virtualmachines/restart", "update"},
		{"subresources.kubevirt.io", "virtualmachineinstances/vnc", "get"}, {"subresources.kubevirt.io", "virtualmachineinstances/console", "get"},
	} {
		if !allowed(rules, want.group, want.resource, want.verb) {
			t.Errorf("missing %s %s/%s", want.verb, want.group, want.resource)
		}
	}
	if len(rules) != 7 {
		t.Errorf("rule count %d", len(rules))
	}
	for _, rule := range rules {
		if contains(rule.Resources, "secrets") {
			t.Errorf("the proxy needs no secrets: %+v", rule)
		}
	}
}

func TestProxyRulesNeedNoDelete(t *testing.T) {
	for _, rule := range ProxyRules() {
		if contains(rule.Verbs, "delete") || contains(rule.Verbs, "*") {
			t.Errorf("rule %+v", rule)
		}
	}
}
