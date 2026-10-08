package operator

import "testing"

func TestOperatorRoleCoversBackup(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []struct{ group, resource, verb string }{
		{"velero.io", "backups", "create"},
		{"velero.io", "schedules", "patch"},
		{"velero.io", "restores", "delete"},
		{"velero.io", "backupstoragelocations", "patch"},
		{"velero.io", "backupstoragelocations", "delete"},
		{"velero.io", "backups", "get"},
		{"velero.io", "deletebackuprequests", "delete"},
		{"", "secrets", "delete"},
		{"", "configmaps", "delete"},
		{"velero.io", "deletebackuprequests", "create"},
		{"velero.io", "datauploads", "list"},
		{"cdi.kubevirt.io", "datavolumes", "delete"},
		{"", "persistentvolumeclaims", "delete"},
		{"", "secrets", "patch"},
		{"kubevirt.io", "virtualmachines", "delete"},
	} {
		if !allows(role, rule.group, rule.resource, rule.verb) {
			t.Errorf("role must allow %s %s/%s", rule.verb, rule.group, rule.resource)
		}
	}
	for _, rule := range []struct{ group, resource, verb string }{
		{"velero.io", "datauploads", "delete"},
		{"velero.io", "backups", "delete"},
		{"velero.io", "backups", "patch"},
		{"velero.io", "schedules", "watch"},
		{"velero.io", "restores", "watch"},
	} {
		if allows(role, rule.group, rule.resource, rule.verb) {
			t.Errorf("role must not allow %s %s/%s", rule.verb, rule.group, rule.resource)
		}
	}
}
