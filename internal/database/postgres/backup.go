package postgres

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

func PreUpgradeBackup(s engine.Spec, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Backup",
		"metadata": map[string]any{"name": name, "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec": map[string]any{
			"method":              "plugin",
			"cluster":             map[string]any{"name": s.Name},
			"pluginConfiguration": map[string]any{"name": PluginName},
		},
	}}
}

func BackupState(obj *unstructured.Unstructured) (*time.Time, bool) {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	if phase == "failed" {
		return nil, true
	}
	if phase != "completed" {
		return nil, false
	}
	stopped, _, _ := unstructured.NestedString(obj.Object, "status", "stoppedAt")
	at, err := time.Parse(time.RFC3339, stopped)
	if err != nil {
		return nil, false
	}
	return &at, false
}
