package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func tenancyRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"tenants", "tenants/status", "projects", "projects/status", "networks", "networks/status", "subnets", "subnets/status"}, Verbs: standardVerbs()},
		{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"elasticips"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: standardVerbsWithoutDelete()},
		{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: standardVerbs()},
		{APIGroups: []string{rbacv1.GroupName}, Resources: []string{"clusterroles", "clusterrolebindings", "rolebindings"}, Verbs: standardVerbs()},
		{APIGroups: []string{"aaq.kubevirt.io"}, Resources: []string{"applicationawareresourcequotas"}, Verbs: standardVerbs()},
	}
}
