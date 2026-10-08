package images

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func decideCron(s Subject, w World) Plan {
	plan := Plan{Apply: []*unstructured.Unstructured{DataImportCron(s)}}
	if w.DataVolume != nil && w.DataVolume.GetName() == ImportName(s.Name) {
		plan.Delete = append(plan.Delete, w.DataVolume.DeepCopy())
	}
	dataVolume, digest, imported := CronImport(orEmpty(w.Cron))
	if !imported {
		plan.Result = PhaseOfCron(w.Cron, nil, nil)
		return plan
	}
	snapshot := findNamed(w.Snapshots, dataVolume)
	alias := findNamed(w.DataSources, s.Name)
	plan.Result = PhaseOfCron(w.Cron, alias, snapshot)
	if w.DataVolume != nil && w.DataVolume.GetName() == dataVolume {
		plan.Result.Progress = percentOf(w.DataVolume)
	}
	if snapshot != nil && !SnapshotReady(snapshot) && SnapshotError(snapshot) != "" {
		plan.Result = snapshotFailed(snapshot).Result
		plan.Requeue = snapshotFailureBackoff
		return plan
	}
	if snapshot == nil || !SnapshotReady(snapshot) {
		return plan
	}

	kept := retain(w.Snapshots, dataVolume, snapshotOf(alias), Keep(s.Spec))
	versions := versionsOf(kept, Keep(s.Spec), cronDigests(w, dataVolume, digest))
	stale, blocked := staleDataSources(w, s, namesOf(kept))
	plan.Apply = append(plan.Apply, versionDataSources(s, readyOnly(kept))...)
	plan.Delete = append(plan.Delete, stale...)
	if blocked {
		plan.Requeue = pruneRetry
	}
	plan.Result.Versions = versions
	if plan.Result.Phase == v1alpha1.ImagePhaseReady {
		plan.Result.Digest = digest
		plan.Result.SizeBytes = RestoreBytes(snapshot)
		plan.Result.DataSource = s.Name
	}
	return plan
}

func cronDigests(w World, current, currentDigest string) func(unstructured.Unstructured) string {
	return func(snapshot unstructured.Unstructured) string {
		if snapshot.GetName() == current {
			return currentDigest
		}
		for _, version := range w.Prev.Versions {
			if version.DataSource == snapshot.GetName() {
				return version.Digest
			}
		}
		return snapshot.GetName()
	}
}

func orEmpty(obj *unstructured.Unstructured) *unstructured.Unstructured {
	if obj == nil {
		return &unstructured.Unstructured{Object: map[string]any{}}
	}
	return obj
}

func findNamed(objects []unstructured.Unstructured, name string) *unstructured.Unstructured {
	for i := range objects {
		if objects[i].GetName() == name {
			return &objects[i]
		}
	}
	return nil
}
