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
		{"velero.io", "backupstoragelocations", "update"},
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
	if allows(role, "velero.io", "datauploads", "delete") {
		t.Error("role must not delete datauploads")
	}
}
