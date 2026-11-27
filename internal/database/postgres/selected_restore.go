package postgres

import (
	"reflect"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

func MatchesSelectedRestore(s engine.Spec, cluster, store *unstructured.Unstructured) bool {
	bootstrap, _, _ := unstructured.NestedMap(cluster.Object, "spec", "bootstrap")
	recovery, _, _ := unstructured.NestedMap(bootstrap, "recovery")
	target, _, err := unstructured.NestedMap(recovery, "recoveryTarget")
	if err != nil || len(bootstrap) != 1 || recovery["backup"] != nil || recovery["volumeSnapshots"] != nil || !reflect.DeepEqual(target, recoveryTarget(s.Restore)) {
		return false
	}
	source, _, _ := unstructured.NestedString(cluster.Object, "spec", "bootstrap", "recovery", "source")
	external, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "externalClusters")
	if len(external) == 1 {
		entry, _ := external[0].(map[string]any)
		plugin, _ := entry["plugin"].(map[string]any)
		if plugin["enabled"] == true {
			delete(plugin, "enabled")
		}
		if plugin["isWALArchiver"] == false {
			delete(plugin, "isWALArchiver")
		}
	}
	if source != "source" || !reflect.DeepEqual(external, externalClusters(s)) {
		return false
	}
	backup, prefix, _ := s.Restore.BackupSource(s.Backup)
	s.Backup = backup
	expected := objectStore(s, engine.Named(s.Name, "source"), prefix)
	return MatchesSelectedStore(expected, store)
}

func MatchesSelectedStore(expected, store *unstructured.Unstructured) bool {
	for _, field := range []string{"destinationPath", "endpointURL", "s3Credentials"} {
		got, found, err := unstructured.NestedFieldNoCopy(store.Object, "spec", "configuration", field)
		want, _, _ := unstructured.NestedFieldNoCopy(expected.Object, "spec", "configuration", field)
		if !found || err != nil || !reflect.DeepEqual(got, want) {
			return false
		}
	}
	return true
}
