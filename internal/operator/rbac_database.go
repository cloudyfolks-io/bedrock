package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func databaseRules() []rbacv1.PolicyRule {
	read := []string{"get", "list", "watch"}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"postgresql.cnpg.io"}, Resources: []string{"backups", "clusters", "scheduledbackups"}, Verbs: standardVerbs()},
		{APIGroups: []string{"barmancloud.cnpg.io"}, Resources: []string{"objectstores"}, Verbs: standardVerbs()},
		{APIGroups: []string{""}, Resources: []string{"configmaps", "persistentvolumeclaims", "resourcequotas", "secrets", "serviceaccounts", "services"}, Verbs: standardVerbs()},
		{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: standardVerbs()},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: standardVerbs()},
		{APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificates"}, Verbs: standardVerbs()},
		{APIGroups: []string{"node.k8s.io"}, Resources: []string{"runtimeclasses"}, Verbs: read},
	}
}
