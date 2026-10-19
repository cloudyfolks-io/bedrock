package mysql

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPreUpgradeBackupUsesTheRgwStorage(t *testing.T) {
	obj := PreUpgradeBackup(bedrockSpec(), testUID+"-pre-100")
	cluster, _, _ := unstructured.NestedString(obj.Object, "spec", "clusterName")
	storage, _, _ := unstructured.NestedString(obj.Object, "spec", "storageName")
	if obj.GetKind() != "PerconaServerMySQLBackup" || obj.GetName() != testUID+"-pre-100" || cluster != testUID || storage != "rgw" || obj.GetNamespace() != "bedrock-dbaas" {
		t.Fatalf("backup %v", obj.Object)
	}
}

func TestBackupStateReadsTheState(t *testing.T) {
	completed := time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status map[string]any
		done   bool
		failed bool
	}{
		{"succeeded", map[string]any{"state": "Succeeded", "completed": completed.Format(time.RFC3339)}, true, false},
		{"succeeded without a time", map[string]any{"state": "Succeeded"}, false, false},
		{"running", map[string]any{"state": "Running"}, false, false},
		{"failed", map[string]any{"state": "Failed"}, false, true},
		{"error", map[string]any{"state": "Error"}, false, true},
		{"empty", nil, false, false},
	}
	for _, c := range cases {
		obj := &unstructured.Unstructured{Object: map[string]any{}}
		if c.status != nil {
			obj.Object["status"] = c.status
		}
		at, failed := BackupState(obj)
		if (at != nil) != c.done || failed != c.failed || at != nil && !at.Equal(completed) {
			t.Errorf("%s: %v %v", c.name, at, failed)
		}
	}
}
