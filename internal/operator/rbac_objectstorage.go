package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func objectStorageRules() []rbacv1.PolicyRule {
	write := []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"ceph.rook.io"}, Resources: []string{"cephobjectstores"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"ceph.rook.io"}, Resources: []string{"cephobjectstoreusers"}, Verbs: write},
		{APIGroups: []string{"objectbucket.io"}, Resources: []string{"objectbucketclaims"}, Verbs: write},
	}
}
