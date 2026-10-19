package container

import (
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

type grant struct{ group, resource, verb string }

func grants(rules []rbacv1.PolicyRule) []grant {
	var out []grant
	for _, rule := range rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					out = append(out, grant{group, resource, verb})
				}
			}
		}
	}
	return out
}

func requireExactly(t *testing.T, rules []rbacv1.PolicyRule, want []grant) {
	t.Helper()
	got := grants(rules)
	for _, g := range want {
		if !slices.Contains(got, g) {
			t.Errorf("the rules lack %+v", g)
		}
	}
	for _, g := range got {
		if !slices.Contains(want, g) {
			t.Errorf("the rules grant more than needed: %+v", g)
		}
	}
}

func TestOperatorRules(t *testing.T) {
	requireExactly(t, OperatorRules(), []grant{
		{"apps", "deployments", "create"}, {"apps", "deployments", "get"}, {"apps", "deployments", "list"}, {"apps", "deployments", "patch"}, {"apps", "deployments", "watch"},
		{"apps", "replicasets", "list"}, {"apps", "replicasets", "watch"},
		{"autoscaling", "horizontalpodautoscalers", "create"}, {"autoscaling", "horizontalpodautoscalers", "delete"}, {"autoscaling", "horizontalpodautoscalers", "get"},
		{"autoscaling", "horizontalpodautoscalers", "list"}, {"autoscaling", "horizontalpodautoscalers", "patch"}, {"autoscaling", "horizontalpodautoscalers", "watch"},
		{"node.k8s.io", "runtimeclasses", "get"}, {"node.k8s.io", "runtimeclasses", "list"}, {"node.k8s.io", "runtimeclasses", "watch"},
		{"batch", "jobs", "create"}, {"batch", "jobs", "delete"}, {"batch", "jobs", "get"}, {"batch", "jobs", "list"}, {"batch", "jobs", "watch"},
		{"", "configmaps", "create"}, {"", "configmaps", "get"}, {"", "configmaps", "patch"},
		{"", "pods", "list"}, {"", "pods", "watch"},
		{"kubevirt.io", "virtualmachineinstances", "list"},
		{"", "secrets", "get"},
		{"bedrock.cloudyfolks.io", "containers/finalizers", "update"},
		{"bedrock.cloudyfolks.io", "builds/finalizers", "update"},
	})
}

func TestProxyRules(t *testing.T) {
	requireExactly(t, ProxyRules(), []grant{
		{"bedrock.cloudyfolks.io", "containers", "get"}, {"bedrock.cloudyfolks.io", "containers", "patch"},
		{"apps", "deployments", "get"}, {"apps", "replicasets", "list"},
		{"bedrock.cloudyfolks.io", "builds", "get"},
		{"batch", "jobs", "get"},
		{"", "pods", "list"},
		{"", "pods/log", "get"},
		{"", "pods/exec", "create"},
	})
	for _, rule := range ProxyRules() {
		if slices.Contains(rule.Resources, "secrets") || slices.Contains(rule.Verbs, "delete") || slices.Contains(rule.Verbs, "*") {
			t.Errorf("rule %+v", rule)
		}
	}
}
