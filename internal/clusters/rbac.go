package clusters

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func rule(group string, resources []string, verbs ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: resources, Verbs: verbs}
}

func OperatorRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("k0smotron.io", []string{"clusters", "jointokenrequests"}, "create", "delete", "get", "list", "patch", "watch"),
		rule("apps", []string{"statefulsets"}, "get", "list", "watch"),
		rule("", []string{"persistentvolumeclaims"}, "get", "list", "watch"),
		rule("", []string{"secrets"}, "create", "delete", "get", "patch"),
		rule("bedrock.cloudyfolks.io", []string{"kubernetesclusters/finalizers"}, "update"),
	}
}
