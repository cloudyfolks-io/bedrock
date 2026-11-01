package compute

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)


const (
	importedContentName  = "snapcontent-7d1f0c2e"
	importedSnapshotName = "snap1"
	importedHandle       = "0001-0009-rook-ceph-0000000000000002-6b1c7a3e-0f5d-4c1e-9a52-3f1d0c8e7b21"
)

func importedContent(ns string) *unstructured.Unstructured {
	obj := emptyOf(csiSnapshotContentGVK)
	obj.SetName(importedContentName)
	obj.Object["spec"] = map[string]any{
		"driver":                  rbdProvisioner,
		"deletionPolicy":          "Retain",
		"sourceVolumeMode":        "Block",
		"volumeSnapshotClassName": "block",
		"source":                  map[string]any{"snapshotHandle": importedHandle},
		"volumeSnapshotRef": map[string]any{
			"apiVersion": "snapshot.storage.k8s.io/v1",
			"kind":       "VolumeSnapshot",
			"name":       importedSnapshotName,
			"namespace":  ns,
		},
	}
	return obj
}

func importedSnapshot(ns string) *unstructured.Unstructured {
	obj := emptyOf(csiSnapshotGVK)
	obj.SetNamespace(ns)
	obj.SetName(importedSnapshotName)
	obj.SetLabels(v1alpha1.TenantLabels("VolumeSnapshot", importedSnapshotName, "acme", "web"))
	obj.Object["spec"] = map[string]any{
		"volumeSnapshotClassName": "block",
		"source":                  map[string]any{"volumeSnapshotContentName": importedContentName},
	}
	return obj
}

func TestVolumeSnapshotAdoptsAnImportedStaticSnapshot(t *testing.T) {
	c, _ := startEnv(t)
	ensureStorageClass(t, c, "block", rbdProvisioner, true)
	ensureSnapshotClass(t, c, "block", rbdProvisioner, nil)
	ns := uniqueNamespace(t, c)
	createVolume(t, c, ns, "data1")
	createPVC(t, c, ns, "data1", 10, "block")
	createObject(t, c, importedContent(ns))
	csi := importedSnapshot(ns)
	createObject(t, c, csi)
	csi.Object["status"] = map[string]any{
		"boundVolumeSnapshotContentName": importedContentName,
		"readyToUse":                     true,
		"restoreSize":                    "10Gi",
	}
	must(t, c.Status().Update(context.Background(), csi))
	createObject(t, c, &v1alpha1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: importedSnapshotName},
		Spec:       v1alpha1.VolumeSnapshotSpec{VolumeRef: "data1"},
	})
	r := &VolumeSnapshotReconciler{Client: c}
	reconcileSnapshot(t, r, ns)

	snapshot := requireSnapshotReason(t, c, ns, "Ready")
	if snapshot.Status.Phase != "Ready" || snapshot.Status.SizeBytes != 10<<30 {
		t.Fatalf("status %+v", snapshot.Status)
	}
	adopted := requirePresent(t, c, csiSnapshotGVK, ns, importedSnapshotName)
	owner := metav1.GetControllerOf(adopted)
	if owner == nil || owner.Kind != "VolumeSnapshot" || owner.UID != snapshot.UID {
		t.Fatalf("owner %+v, want the Bedrock snapshot %s", owner, snapshot.UID)
	}
	if bound, _, _ := unstructured.NestedString(adopted.Object, "status", "boundVolumeSnapshotContentName"); bound != importedContentName {
		t.Fatalf("bound content %q", bound)
	}
	if source, _, _ := unstructured.NestedString(adopted.Object, "spec", "source", "volumeSnapshotContentName"); source != importedContentName {
		t.Fatalf("source %q: the static source changed", source)
	}
	content := requirePresent(t, c, csiSnapshotContentGVK, "", importedContentName)
	if handle, _, _ := unstructured.NestedString(content.Object, "spec", "source", "snapshotHandle"); handle != importedHandle {
		t.Fatalf("handle %q", handle)
	}
	if meta.FindStatusCondition(snapshot.Status.Conditions, v1alpha1.ConditionReady).Status != metav1.ConditionTrue {
		t.Fatalf("conditions %+v", snapshot.Status.Conditions)
	}
}

func TestVolumeSnapshotSizesAStaticSnapshotWithoutRestoreSizeFromTheClaim(t *testing.T) {
	c, _ := startEnv(t)
	ensureStorageClass(t, c, "block", rbdProvisioner, true)
	ensureSnapshotClass(t, c, "block", rbdProvisioner, nil)
	ns := uniqueNamespace(t, c)
	createVolume(t, c, ns, "data1")
	createPVC(t, c, ns, "data1", 10, "block")
	stale := emptyOf(csiSnapshotContentGVK)
	stale.SetName(importedContentName)
	_ = client.IgnoreNotFound(c.Delete(context.Background(), stale))
	createObject(t, c, importedContent(ns))
	csi := importedSnapshot(ns)
	createObject(t, c, csi)
	csi.Object["status"] = map[string]any{
		"boundVolumeSnapshotContentName": importedContentName,
		"readyToUse":                     true,
	}
	must(t, c.Status().Update(context.Background(), csi))
	createObject(t, c, &v1alpha1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: importedSnapshotName},
		Spec:       v1alpha1.VolumeSnapshotSpec{VolumeRef: "data1"},
	})
	reconcileSnapshot(t, &VolumeSnapshotReconciler{Client: c}, ns)

	snapshot := requireSnapshotReason(t, c, ns, "Ready")
	if snapshot.Status.Phase != "Ready" || snapshot.Status.SizeBytes != 10<<30 {
		t.Fatalf("status %+v", snapshot.Status)
	}
}

func TestVolumeSnapshotSizePrefersTheContentAnnotation(t *testing.T) {
	c, _ := startEnv(t)
	ensureStorageClass(t, c, "block", rbdProvisioner, true)
	ensureSnapshotClass(t, c, "block", rbdProvisioner, nil)
	ns := uniqueNamespace(t, c)
	createVolume(t, c, ns, "data1")
	createPVC(t, c, ns, "data1", 10, "block")
	stale := emptyOf(csiSnapshotContentGVK)
	stale.SetName(importedContentName)
	_ = client.IgnoreNotFound(c.Delete(context.Background(), stale))
	content := importedContent(ns)
	content.SetAnnotations(map[string]string{sizeBytesAnnotation: "4096"})
	createObject(t, c, content)
	csi := importedSnapshot(ns)
	createObject(t, c, csi)
	csi.Object["status"] = map[string]any{
		"boundVolumeSnapshotContentName": importedContentName,
		"readyToUse":                     true,
		"restoreSize":                    "10Gi",
	}
	must(t, c.Status().Update(context.Background(), csi))
	createObject(t, c, &v1alpha1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: importedSnapshotName},
		Spec:       v1alpha1.VolumeSnapshotSpec{VolumeRef: "data1"},
	})
	reconcileSnapshot(t, &VolumeSnapshotReconciler{Client: c}, ns)

	if snapshot := requireSnapshotReason(t, c, ns, "Ready"); snapshot.Status.SizeBytes != 4096 {
		t.Fatalf("status %+v", snapshot.Status)
	}
}

func TestParseSizeAnnotationIgnoresInvalidValues(t *testing.T) {
	for in, want := range map[string]int64{"4096": 4096, "": 0, "abc": 0, "-5": 0, "0": 0, "1.5": 0} {
		if got := parseSizeAnnotation(in); got != want {
			t.Fatalf("%q: got %d, want %d", in, got, want)
		}
	}
}

func TestSnapshotConditionReportsAnUnknownSize(t *testing.T) {
	cond := snapshotCondition(v1alpha1.VolumeSnapshot{}, snapshotOutcome{Phase: v1alpha1.VolumeSnapshotPhaseReady})
	if cond.Reason != "SizeUnknown" || cond.Status != metav1.ConditionTrue {
		t.Fatalf("condition %+v", cond)
	}
}
