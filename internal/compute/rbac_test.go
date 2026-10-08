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

type grant struct{ group, resource, verb string }

func requireGrants(t *testing.T, rules []rbacv1.PolicyRule, want, refused []grant) {
	t.Helper()
	for _, g := range want {
		if !allowed(rules, g.group, g.resource, g.verb) {
			t.Errorf("missing %s %s/%s", g.verb, g.group, g.resource)
		}
	}
	for _, g := range refused {
		if allowed(rules, g.group, g.resource, g.verb) {
			t.Errorf("%s %s/%s is not used by the code", g.verb, g.group, g.resource)
		}
	}
}

func TestOperatorRules(t *testing.T) {
	rules := OperatorRules()
	requireGrants(t, rules, []grant{
		{"kubevirt.io", "virtualmachines", "create"}, {"kubevirt.io", "virtualmachines", "patch"}, {"kubevirt.io", "virtualmachines", "watch"},
		{"kubevirt.io", "virtualmachineinstances", "watch"}, {"kubevirt.io", "virtualmachineinstances", "get"},
		{"snapshot.kubevirt.io", "virtualmachinesnapshots", "list"}, {"snapshot.kubevirt.io", "virtualmachinesnapshots", "delete"},
		{"instancetype.kubevirt.io", "virtualmachineclusterinstancetypes", "get"}, {"instancetype.kubevirt.io", "virtualmachineclusterpreferences", "get"},
		{"subresources.kubevirt.io", "virtualmachineinstances/guestosinfo", "get"},
		{"cdi.kubevirt.io", "datavolumes", "create"}, {"cdi.kubevirt.io", "datavolumes", "watch"},
		{"", "persistentvolumeclaims", "patch"}, {"", "persistentvolumeclaims", "watch"},
		{"", "secrets", "create"}, {"", "secrets", "patch"}, {"", "secrets", "get"},
		{"", "pods", "patch"}, {"", "pods", "watch"},
		{"snapshot.storage.k8s.io", "volumesnapshots", "create"}, {"snapshot.storage.k8s.io", "volumesnapshots", "watch"},
		{"snapshot.storage.k8s.io", "volumesnapshotclasses", "list"}, {"storage.k8s.io", "storageclasses", "watch"},
		{"fabric.cloudyfolks.io", "security-groups", "get"}, {"bedrock.cloudyfolks.io", "volumes/finalizers", "update"},
	}, []grant{
		{"", "secrets", "list"}, {"", "secrets", "watch"}, {"", "secrets", "update"}, {"", "secrets", "delete"},
		{"kubevirt.io", "virtualmachines", "delete"}, {"kubevirt.io", "virtualmachines", "update"},
		{"kubevirt.io", "virtualmachineinstancemigrations", "get"}, {"kubevirt.io", "virtualmachineinstancemigrations", "create"},
		{"cdi.kubevirt.io", "datavolumes", "delete"}, {"cdi.kubevirt.io", "datavolumes", "update"}, {"cdi.kubevirt.io", "datavolumes", "patch"},
		{"cdi.kubevirt.io", "datasources", "get"},
		{"snapshot.storage.k8s.io", "volumesnapshots", "delete"}, {"snapshot.storage.k8s.io", "volumesnapshots", "patch"}, {"snapshot.storage.k8s.io", "volumesnapshots", "update"},
		{"", "pods", "delete"}, {"", "persistentvolumeclaims", "delete"},
	})
	for _, rule := range rules {
		if contains(rule.Resources, "datavolumes/source") || contains(rule.Resources, "*") || contains(rule.Verbs, "*") || contains(rule.APIGroups, "*") {
			t.Errorf("forbidden rule %+v", rule)
		}
	}
}

func TestProxyRules(t *testing.T) {
	rules := ProxyRules()
	requireGrants(t, rules, []grant{
		{"bedrock.cloudyfolks.io", "virtualmachines", "get"}, {"bedrock.cloudyfolks.io", "virtualmachines", "patch"},
		{"", "namespaces", "get"}, {"kubevirt.io", "virtualmachineinstancemigrations", "create"},
		{"snapshot.kubevirt.io", "virtualmachinesnapshots", "create"}, {"snapshot.kubevirt.io", "virtualmachinesnapshots", "list"},
		{"subresources.kubevirt.io", "virtualmachines/restart", "update"},
		{"subresources.kubevirt.io", "virtualmachineinstances/vnc", "get"}, {"subresources.kubevirt.io", "virtualmachineinstances/console", "get"},
	}, []grant{
		{"bedrock.cloudyfolks.io", "virtualmachines", "list"},
		{"kubevirt.io", "virtualmachineinstancemigrations", "get"}, {"kubevirt.io", "virtualmachineinstancemigrations", "list"},
		{"snapshot.kubevirt.io", "virtualmachinesnapshots", "get"},
	})
	if len(rules) != 7 {
		t.Errorf("rule count %d", len(rules))
	}
	for _, rule := range rules {
		if contains(rule.Resources, "secrets") || contains(rule.Verbs, "delete") || contains(rule.Verbs, "*") {
			t.Errorf("rule %+v", rule)
		}
	}
}
