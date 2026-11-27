package compute

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const migrationPhasePending = "Pending"

func migrationTarget(migration unstructured.Unstructured) string {
	name, _, _ := unstructured.NestedString(migration.Object, "spec", "vmiName")
	return name
}

func belongsTo(vm v1alpha1.VirtualMachine, migration unstructured.Unstructured) bool {
	return migration.GetNamespace() == vm.Namespace && migrationTarget(migration) == vm.Name
}

func newer(candidate, current unstructured.Unstructured) bool {
	candidateTime, currentTime := candidate.GetCreationTimestamp().Time, current.GetCreationTimestamp().Time
	if candidateTime.Equal(currentTime) {
		return candidate.GetName() > current.GetName()
	}
	return candidateTime.After(currentTime)
}

func LatestMigration(vm v1alpha1.VirtualMachine, migrations []unstructured.Unstructured) *v1alpha1.VMMigration {
	var latest *unstructured.Unstructured
	for index := range migrations {
		if belongsTo(vm, migrations[index]) && (latest == nil || newer(migrations[index], *latest)) {
			latest = &migrations[index]
		}
	}
	if latest == nil {
		return nil
	}
	phase, _, _ := unstructured.NestedString(latest.Object, "status", "phase")
	if phase == "" {
		phase = migrationPhasePending
	}
	message, _, _ := unstructured.NestedString(latest.Object, "status", "migrationState", "failureReason")
	started := latest.GetCreationTimestamp()
	return &v1alpha1.VMMigration{Name: latest.GetName(), Phase: phase, StartedAt: &started, Message: message}
}

func migrationOwner(_ context.Context, obj client.Object) []reconcile.Request {
	migration, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	name := migrationTarget(*migration)
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: migration.GetNamespace(), Name: name}}}
}
