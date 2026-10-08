package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func backupRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"velero.io"}, Resources: []string{"backupstoragelocations", "restores", "schedules"}, Verbs: []string{"create", "delete", "get", "patch"}},
		{APIGroups: []string{"velero.io"}, Resources: []string{"backups"}, Verbs: []string{"create", "get", "list"}},
		{APIGroups: []string{"velero.io"}, Resources: []string{"deletebackuprequests"}, Verbs: []string{"create", "delete", "list"}},
		{APIGroups: []string{"velero.io"}, Resources: []string{"datauploads"}, Verbs: []string{"list"}},
		{APIGroups: []string{"cdi.kubevirt.io"}, Resources: []string{"datavolumes"}, Verbs: []string{"delete", "list"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, Verbs: []string{"delete", "list"}},
	}
}
