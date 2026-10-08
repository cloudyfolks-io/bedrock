package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func backupRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"velero.io"}, Resources: []string{"backups", "backupstoragelocations", "deletebackuprequests", "restores", "schedules"}, Verbs: []string{"create", "delete", "get", "list", "patch", "update", "watch"}},
		{APIGroups: []string{"velero.io"}, Resources: []string{"datauploads"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"cdi.kubevirt.io"}, Resources: []string{"datavolumes"}, Verbs: []string{"delete", "get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, Verbs: []string{"delete", "get", "list", "watch"}},
	}
}
