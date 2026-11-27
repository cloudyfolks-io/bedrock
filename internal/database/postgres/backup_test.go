package postgres

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPreUpgradeBackupTargetsTheClusterWithThePlugin(t *testing.T) {
	obj := PreUpgradeBackup(bedrockSpec(), testUID+"-pre-100")
	method, _, _ := unstructured.NestedString(obj.Object, "spec", "method")
	cluster, _, _ := unstructured.NestedString(obj.Object, "spec", "cluster", "name")
	plugin, _, _ := unstructured.NestedString(obj.Object, "spec", "pluginConfiguration", "name")
	if obj.GetKind() != "Backup" || obj.GetName() != testUID+"-pre-100" || obj.GetNamespace() != "bedrock-dbaas" || method != "plugin" || cluster != testUID || plugin != PluginName {
		t.Fatalf("backup %v", obj.Object)
	}
	if obj.GetLabels()["bedrock.cloudyfolks.io/name"] != "orders" {
		t.Fatalf("labels %v", obj.GetLabels())
	}
}

func TestBackupStateReadsThePhase(t *testing.T) {
	stopped := time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		status    map[string]any
		completed bool
		failed    bool
	}{
		{"completed", map[string]any{"phase": "completed", "stoppedAt": stopped.Format(time.RFC3339)}, true, false},
		{"completed without a time", map[string]any{"phase": "completed"}, false, false},
		{"running", map[string]any{"phase": "running"}, false, false},
		{"failed", map[string]any{"phase": "failed"}, false, true},
		{"empty", nil, false, false},
	}
	for _, c := range cases {
		obj := &unstructured.Unstructured{Object: map[string]any{}}
		if c.status != nil {
			obj.Object["status"] = c.status
		}
		at, failed := BackupState(obj)
		if (at != nil) != c.completed || failed != c.failed || at != nil && !at.Equal(stopped) {
			t.Errorf("%s: %v %v", c.name, at, failed)
		}
	}
}
