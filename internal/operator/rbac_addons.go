package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func addonsRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"serviceaccounts", "services", "configmaps"}, Verbs: standardVerbs()},
		{APIGroups: []string{"apps"}, Resources: []string{"daemonsets", "statefulsets"}, Verbs: standardVerbs()},
		{APIGroups: []string{rbacv1.GroupName}, Resources: []string{"clusterroles", "clusterrolebindings"}, Verbs: standardVerbs()},
		{APIGroups: []string{"node.k8s.io"}, Resources: []string{"runtimeclasses"}, Verbs: standardVerbs()},
		{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"addons", "addons/status", "addons/finalizers"}, Verbs: standardVerbs()},
	}
}
