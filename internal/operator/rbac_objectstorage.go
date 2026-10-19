package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func objectStorageRules() []rbacv1.PolicyRule {
	write := []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	read := []string{"get", "list", "watch"}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"ceph.rook.io"}, Resources: []string{"cephobjectstores"}, Verbs: read},
		{APIGroups: []string{"ceph.rook.io"}, Resources: []string{"cephobjectstoreaccounts", "cephobjectstoreusers"}, Verbs: write},
		{APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificates"}, Verbs: write},
		{APIGroups: []string{"traefik.io"}, Resources: []string{"ingressroutes"}, Verbs: write},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "delete", "get", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: write},
		{APIGroups: []string{"objectbucket.io"}, Resources: []string{"objectbucketclaims"}, Verbs: read},
	}
}
