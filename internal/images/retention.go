package images

import (
	"slices"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const pruneRetry = 30 * time.Second

func ownedSnapshots(snapshots []unstructured.Unstructured, owner types.UID) []unstructured.Unstructured {
	var owned []unstructured.Unstructured
	for _, snapshot := range snapshots {
		if slices.ContainsFunc(snapshot.GetOwnerReferences(), func(ref metav1.OwnerReference) bool { return ref.UID == owner }) {
			owned = append(owned, snapshot)
		}
	}
	return owned
}

func snapshotOf(source *unstructured.Unstructured) string {
	if source == nil {
		return ""
	}
	name, _, _ := unstructured.NestedString(source.Object, "spec", "source", "snapshot", "name")
	return name
}

func retain(snapshots []unstructured.Unstructured, current, aliasTarget string, keep int32) []unstructured.Unstructured {
	newestFirst := slices.Clone(snapshots)
	sort.SliceStable(newestFirst, func(i, j int) bool { return newer(&newestFirst[i], &newestFirst[j]) })
	wanted := map[string]bool{current: true, aliasTarget: true}
	for i := 0; i < len(newestFirst) && i < int(keep); i++ {
		wanted[newestFirst[i].GetName()] = true
	}
	var kept []unstructured.Unstructured
	if index := slices.IndexFunc(newestFirst, func(s unstructured.Unstructured) bool { return s.GetName() == current }); index >= 0 {
		kept = append(kept, newestFirst[index])
	}
	for _, snapshot := range newestFirst {
		if wanted[snapshot.GetName()] && snapshot.GetName() != current {
			kept = append(kept, snapshot)
		}
	}
	return kept
}

func readyOnly(snapshots []unstructured.Unstructured) []unstructured.Unstructured {
	var ready []unstructured.Unstructured
	for i := range snapshots {
		if SnapshotReady(&snapshots[i]) {
			ready = append(ready, snapshots[i])
		}
	}
	return ready
}

func versionsOf(kept []unstructured.Unstructured, keep int32, digestOf func(unstructured.Unstructured) string) []v1alpha1.ImageVersion {
	var versions []v1alpha1.ImageVersion
	for _, snapshot := range readyOnly(kept) {
		if len(versions) == int(keep) {
			break
		}
		versions = append(versions, v1alpha1.ImageVersion{
			Digest:     digestOf(snapshot),
			Time:       ptrTime(snapshot.GetCreationTimestamp()),
			DataSource: snapshot.GetName(),
		})
	}
	return versions
}

func annotatedDigest(snapshot unstructured.Unstructured) string {
	return snapshot.GetAnnotations()[AnnotationDigest]
}

func namesOf(objects []unstructured.Unstructured) []string {
	names := make([]string, 0, len(objects))
	for _, object := range objects {
		names = append(names, object.GetName())
	}
	return names
}

func prune(s Subject, w World, owned, kept []unstructured.Unstructured, reference *unstructured.Unstructured) ([]*unstructured.Unstructured, bool) {
	keptNames := namesOf(kept)
	stale, blocked := staleDataSources(w, s, keptNames)
	for i := range owned {
		candidate := &owned[i]
		if slices.Contains(keptNames, candidate.GetName()) || !older(candidate, reference) {
			continue
		}
		if snapshotReferenced(w, s.Namespace, candidate.GetName()) {
			blocked = true
			continue
		}
		stale = append(stale, candidate.DeepCopy())
	}
	return stale, blocked
}

func staleDataSources(w World, s Subject, keptNames []string) ([]*unstructured.Unstructured, bool) {
	var stale []*unstructured.Unstructured
	blocked := false
	for _, source := range w.DataSources {
		if source.GetName() == s.Name || slices.Contains(keptNames, source.GetName()) {
			continue
		}
		if dataSourceReferenced(w, s.Namespace, source.GetName()) {
			blocked = true
			continue
		}
		stale = append(stale, source.DeepCopy())
	}
	return stale, blocked
}

func older(candidate, reference *unstructured.Unstructured) bool {
	return reference != nil && candidate.GetCreationTimestamp().Time.Before(reference.GetCreationTimestamp().Time)
}

func dataSourceReferenced(w World, namespace, name string) bool {
	return slices.ContainsFunc(w.DataVolumes, func(dv unstructured.Unstructured) bool {
		refKind, _, _ := unstructured.NestedString(dv.Object, "spec", "sourceRef", "kind")
		refName, _, _ := unstructured.NestedString(dv.Object, "spec", "sourceRef", "name")
		return refKind == "DataSource" && refName == name && refNamespace(dv, "sourceRef") == namespace
	})
}

func snapshotReferenced(w World, namespace, snapshot string) bool {
	for _, source := range w.AllDataSources {
		if source.GetNamespace() == namespace && snapshotOf(&source) == snapshot {
			return true
		}
	}
	for _, dv := range w.DataVolumes {
		direct, _, _ := unstructured.NestedString(dv.Object, "spec", "source", "snapshot", "name")
		if direct == snapshot && refNamespace(dv, "source", "snapshot") == namespace {
			return true
		}
	}
	return slices.ContainsFunc(w.Claims, func(claim corev1.PersistentVolumeClaim) bool {
		return claim.Namespace == namespace && claimSourcesSnapshot(claim, snapshot)
	})
}

func refNamespace(dv unstructured.Unstructured, path ...string) string {
	namespace, _, _ := unstructured.NestedString(dv.Object, append([]string{"spec"}, append(path, "namespace")...)...)
	if namespace == "" {
		return dv.GetNamespace()
	}
	return namespace
}

func claimSourcesSnapshot(claim corev1.PersistentVolumeClaim, snapshot string) bool {
	if ref := claim.Spec.DataSource; ref != nil && ref.Kind == "VolumeSnapshot" && ref.Name == snapshot {
		return true
	}
	ref := claim.Spec.DataSourceRef
	return ref != nil && ref.Kind == "VolumeSnapshot" && ref.Name == snapshot
}
