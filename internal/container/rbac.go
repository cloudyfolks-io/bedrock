package container

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func rule(group string, resources []string, verbs ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: resources, Verbs: verbs}
}

func OperatorRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("apps", []string{"deployments"}, "create", "get", "list", "patch", "watch"),
		rule("batch", []string{"jobs"}, "create", "delete", "get", "list", "watch"),
		rule("autoscaling", []string{"horizontalpodautoscalers"}, "create", "delete", "get", "list", "patch", "watch"),
		rule("node.k8s.io", []string{"runtimeclasses"}, "get", "list", "watch"),
		rule("", []string{"configmaps"}, "create", "get", "patch"),
		rule("", []string{"pods"}, "list", "watch"),
		rule("", []string{"secrets"}, "get"),
		rule("bedrock.cloudyfolks.io", []string{"builds/finalizers", "containers/finalizers"}, "update"),
	}
}

func ProxyRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("bedrock.cloudyfolks.io", []string{"builds"}, "get"),
		rule("bedrock.cloudyfolks.io", []string{"containers"}, "get", "patch"),
		rule("", []string{"pods"}, "list"),
		rule("", []string{"pods/log"}, "get"),
		rule("", []string{"pods/exec"}, "create"),
	}
}
