package images

import (
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func decideVolume(s Subject, w World) Plan {
	spec := s.Spec
	hash := SourceHash(spec)
	current := newestWithHash(w.Snapshots, hash)
	switch {
	case w.Cron != nil:
		return Plan{
			Delete:  []*unstructured.Unstructured{w.Cron.DeepCopy()},
			Result:  Result{Phase: v1alpha1.ImagePhasePending, Reason: ReasonImportScheduled},
			Requeue: 2 * time.Second,
		}
	case s.Kind == KindClusterImage && spec.Source.Upload != nil:
		return failed(ReasonUploadNotSupported, "a cluster image cannot take an upload")
	case current != nil && SnapshotReady(current):
		return decideGolden(s, w, *current)
	case current != nil:
		return pending(v1alpha1.ImagePhaseImporting, ReasonSnapshotting, "", 10*time.Second)
	case spec.Source.VolumeRef != nil && w.SourceClaim == nil:
		message := fmt.Sprintf("claim %s not found in %s", spec.Source.VolumeRef.Name, s.Namespace)
		return pending(v1alpha1.ImagePhasePending, ReasonVolumeNotFound, message, 30*time.Second)
	case w.DataVolume == nil:
		return Plan{
			Apply:   []*unstructured.Unstructured{ImportDataVolume(s, importSizeGi(spec, w.SourceClaim))},
			Result:  Result{Phase: v1alpha1.ImagePhasePending, Reason: ReasonImportScheduled},
			Requeue: 5 * time.Second,
		}
	case w.DataVolume.GetAnnotations()[AnnotationSourceHash] != hash:
		return Plan{
			Delete:  []*unstructured.Unstructured{w.DataVolume.DeepCopy()},
			Result:  Result{Phase: v1alpha1.ImagePhasePending, Reason: ReasonSourceChanged},
			Requeue: 2 * time.Second,
		}
	default:
		return decideImport(s, w)
	}
}

func decideImport(s Subject, w World) Plan {
	result := PhaseOfDataVolume(w.DataVolume)
	switch {
	case result.Reason == ReasonSnapshotting && w.ImportClaim == nil:
		return pending(v1alpha1.ImagePhasePending, ReasonClaimNotFound, "the import claim is missing", 0)
	case result.Reason == ReasonSnapshotting:
		digest := DigestOfClaim(s.Spec, w.ImportClaim)
		return Plan{
			Apply:  []*unstructured.Unstructured{GoldenSnapshot(s, ImportName(s.Name), digest)},
			Result: result,
		}
	case result.Phase == v1alpha1.ImagePhaseWaitingForUpload && w.PlatformHost == "":
		result.Reason = ReasonPlatformHostNotSet
	case result.Phase == v1alpha1.ImagePhaseWaitingForUpload:
		result.UploadURL = UploadURL(w.PlatformHost)
	}
	return Plan{Result: result}
}

func decideGolden(s Subject, w World, current unstructured.Unstructured) Plan {
	latest := v1alpha1.ImageVersion{
		Digest:     current.GetAnnotations()[AnnotationDigest],
		Time:       ptrTime(current.GetCreationTimestamp()),
		DataSource: current.GetName(),
	}
	versions := MergeVersions(w.Prev.Versions, &latest, namesOf(w.Snapshots), Keep(s.Spec))
	kept := dataSourcesOf(versions)

	plan := Plan{Apply: versionDataSources(s, versions)}
	plan.Apply = append(plan.Apply, DataSource(s, s.Name, current.GetName()))
	plan.Delete = staleObjects(w, kept, s.Name)

	if aliasReady(w.DataSources, s.Name) {
		plan.Result = Result{
			Phase:      v1alpha1.ImagePhaseReady,
			Reason:     ReasonImported,
			Digest:     latest.Digest,
			SizeBytes:  RestoreBytes(&current),
			DataSource: s.Name,
			Versions:   versions,
		}
		return plan
	}
	plan.Result = Result{Phase: v1alpha1.ImagePhaseImporting, Reason: ReasonWaitingForDataSource, Versions: versions}
	return plan
}

func versionDataSources(s Subject, versions []v1alpha1.ImageVersion) []*unstructured.Unstructured {
	objects := make([]*unstructured.Unstructured, 0, len(versions)+1)
	for _, version := range versions {
		objects = append(objects, DataSource(s, version.DataSource, version.DataSource))
	}
	return objects
}

func staleObjects(w World, kept []string, alias string) []*unstructured.Unstructured {
	var stale []*unstructured.Unstructured
	if w.DataVolume != nil {
		stale = append(stale, w.DataVolume.DeepCopy())
	}
	for _, snapshot := range w.Snapshots {
		if !slices.Contains(kept, snapshot.GetName()) {
			stale = append(stale, snapshot.DeepCopy())
		}
	}
	return append(stale, staleDataSources(w.DataSources, kept, alias)...)
}

func staleDataSources(sources []unstructured.Unstructured, kept []string, alias string) []*unstructured.Unstructured {
	var stale []*unstructured.Unstructured
	for _, source := range sources {
		if source.GetName() != alias && !slices.Contains(kept, source.GetName()) {
			stale = append(stale, source.DeepCopy())
		}
	}
	return stale
}

func newestWithHash(snapshots []unstructured.Unstructured, hash string) *unstructured.Unstructured {
	var newest *unstructured.Unstructured
	for i := range snapshots {
		candidate := &snapshots[i]
		if candidate.GetAnnotations()[AnnotationSourceHash] != hash {
			continue
		}
		if newest == nil || newer(candidate, newest) {
			newest = candidate
		}
	}
	return newest
}

func newer(a, b *unstructured.Unstructured) bool {
	timeA, timeB := a.GetCreationTimestamp().Time, b.GetCreationTimestamp().Time
	if timeA.Equal(timeB) {
		return a.GetName() > b.GetName()
	}
	return timeA.After(timeB)
}

func namesOf(objects []unstructured.Unstructured) map[string]bool {
	names := make(map[string]bool, len(objects))
	for _, object := range objects {
		names[object.GetName()] = true
	}
	return names
}

func dataSourcesOf(versions []v1alpha1.ImageVersion) []string {
	names := make([]string, 0, len(versions))
	for _, version := range versions {
		names = append(names, version.DataSource)
	}
	return names
}

func aliasReady(sources []unstructured.Unstructured, alias string) bool {
	for i := range sources {
		if sources[i].GetName() == alias {
			return conditionStatus(&sources[i], "Ready") == "True"
		}
	}
	return false
}

func importSizeGi(spec v1alpha1.ImageSpec, source *corev1.PersistentVolumeClaim) int32 {
	if spec.Source.VolumeRef == nil || source == nil {
		return SizeGi(spec)
	}
	quantity := source.Spec.Resources.Requests[corev1.ResourceStorage]
	return max(spec.SizeGi, roundUpGi(quantity))
}

func roundUpGi(quantity resource.Quantity) int32 {
	const gi = int64(1) << 30
	return int32((quantity.Value() + gi - 1) / gi)
}

func ptrTime(t metav1.Time) *metav1.Time {
	return &t
}

func failed(reason, message string) Plan {
	return Plan{Result: Result{Phase: v1alpha1.ImagePhaseFailed, Reason: reason, Message: message}}
}

func pending(phase, reason, message string, requeue time.Duration) Plan {
	return Plan{Result: Result{Phase: phase, Reason: reason, Message: message}, Requeue: requeue}
}
