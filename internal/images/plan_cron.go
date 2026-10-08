package images

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func decideCron(s Subject, w World, now time.Time) Plan {
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
	if snapshot == nil || !SnapshotReady(snapshot) {
		return plan
	}

	latest := v1alpha1.ImageVersion{Digest: digest, Time: importTime(w.Cron, now), DataSource: dataVolume}
	versions := MergeVersions(w.Prev.Versions, &latest, namesOf(w.Snapshots), Keep(s.Spec))
	kept := dataSourcesOf(versions)
	plan.Apply = append(plan.Apply, versionDataSources(s, versions)...)
	plan.Delete = append(plan.Delete, staleDataSources(w.DataSources, kept, s.Name)...)
	plan.Result.Versions = versions
	if plan.Result.Phase == v1alpha1.ImagePhaseReady {
		plan.Result.Digest = digest
		plan.Result.SizeBytes = RestoreBytes(snapshot)
		plan.Result.DataSource = s.Name
	}
	return plan
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

func importTime(cron *unstructured.Unstructured, now time.Time) *metav1.Time {
	text, _, _ := unstructured.NestedString(cron.Object, "status", "lastImportTimestamp")
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return &metav1.Time{Time: parsed}
	}
	return &metav1.Time{Time: now}
}
