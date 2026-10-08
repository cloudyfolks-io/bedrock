package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func imagesRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"cdi.kubevirt.io"}, Resources: []string{"datavolumes", "datasources", "dataimportcrons"}, Verbs: standardVerbs()},
		{APIGroups: []string{"snapshot.storage.k8s.io"}, Resources: []string{"volumesnapshots"}, Verbs: standardVerbs()},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims", "namespaces"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: standardVerbs()},
		{APIGroups: []string{"traefik.io"}, Resources: []string{"middlewares"}, Verbs: standardVerbs()},
	}
}
