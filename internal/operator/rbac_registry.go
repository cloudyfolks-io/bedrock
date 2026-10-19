package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func registryRules() []rbacv1.PolicyRule {
	read := []string{"get", "list", "watch"}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"postgresql.cnpg.io"}, Resources: []string{"clusters", "scheduledbackups"}, Verbs: standardVerbs()},
		{APIGroups: []string{"barmancloud.cnpg.io"}, Resources: []string{"objectstores"}, Verbs: standardVerbs()},
		{APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificates"}, Verbs: standardVerbs()},
		{APIGroups: []string{"traefik.io"}, Resources: []string{"ingressroutes", "middlewares"}, Verbs: standardVerbs()},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "delete", "get", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"create", "get", "patch"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: read},
		{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"registries", "registries/finalizers", "registrycredentials", "registrycredentials/finalizers"}, Verbs: standardVerbs()},
	}
}
