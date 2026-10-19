package mysql

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

func PreUpgradeBackup(s engine.Spec, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ps.percona.com/v1alpha1", "kind": BackupGVK.Kind,
		"metadata": map[string]any{"name": name, "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec":     map[string]any{"clusterName": s.Name, "storageName": StorageName},
	}}
}

func BackupState(obj *unstructured.Unstructured) (*time.Time, bool) {
	state, _, _ := unstructured.NestedString(obj.Object, "status", "state")
	switch state {
	case "Failed", "Error":
		return nil, true
	case succeededState:
		completed, _, _ := unstructured.NestedString(obj.Object, "status", "completed")
		if at, err := time.Parse(time.RFC3339, completed); err == nil {
			return &at, false
		}
	}
	return nil, false
}
