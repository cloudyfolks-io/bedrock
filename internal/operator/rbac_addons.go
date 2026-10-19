package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func addonsRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"serviceaccounts", "services", "configmaps"}, Verbs: standardVerbs()},
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get", "create", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "delete", "get", "patch", "update"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: standardVerbs()},
		{APIGroups: []string{rbacv1.GroupName}, Resources: []string{"roles", "rolebindings"}, Verbs: standardVerbs()},
		{APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificates", "issuers", "clusterissuers"}, Verbs: standardVerbs()},
		{APIGroups: []string{"admissionregistration.k8s.io"}, Resources: []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"}, Verbs: standardVerbs()},
		{APIGroups: []string{"apps"}, Resources: []string{"daemonsets", "statefulsets"}, Verbs: standardVerbs()},
		{APIGroups: []string{rbacv1.GroupName}, Resources: []string{"clusterroles", "clusterrolebindings"}, Verbs: standardVerbs()},
		{APIGroups: []string{"node.k8s.io"}, Resources: []string{"runtimeclasses"}, Verbs: standardVerbs()},
		{APIGroups: []string{"monitoring.coreos.com"}, Resources: []string{"prometheusrules"}, Verbs: standardVerbs()},
		{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"addons", "addons/status", "addons/finalizers", "databaseversions", "databaseclasses"}, Verbs: standardVerbs()},
	}
}
