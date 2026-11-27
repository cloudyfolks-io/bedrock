package compute

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestRetainedSnapshotWithoutItsSourceVolume(t *testing.T) {
	c, _ := startEnv(t)
	for _, tc := range []struct {
		name   string
		driver string
		mode   string
	}{
		{"dynamic", rbdProvisioner, "Block"},
		{"static", cephfsProvisioner, "Filesystem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ns := uniqueNamespace(t, c)
			snapshot := &v1alpha1.VolumeSnapshot{
				ObjectMeta: metav1.ObjectMeta{Name: "snap1", Namespace: ns},
				Spec:       v1alpha1.VolumeSnapshotSpec{VolumeRef: "deleted-source"},
			}
			createObject(t, c, snapshot)
			contentName := ns + "-content"
			csi := importedSnapshot(ns)
			csi.SetOwnerReferences([]metav1.OwnerReference{controllerRef("VolumeSnapshot", snapshot)})
			spec := csi.Object["spec"].(map[string]any)
			source := map[string]any{"persistentVolumeClaimName": snapshot.Spec.VolumeRef}
			backend := map[string]any{"volumeHandle": "original-pv-handle"}
			if tc.name == "static" {
				source = map[string]any{"volumeSnapshotContentName": contentName}
				backend = map[string]any{"snapshotHandle": importedHandle}
			}
			spec["source"] = source
			createObject(t, c, csi)
			csi.Object["status"] = map[string]any{"readyToUse": true, "restoreSize": "10Gi", "boundVolumeSnapshotContentName": contentName}
			if tc.name == "static" {
				csi.Object["status"].(map[string]any)["restoreSize"] = "0"
			}
			must(t, c.Status().Update(ctx, csi))
			content := importedContent(ns)
			content.SetName(contentName)
			if tc.name == "static" {
				content.SetAnnotations(map[string]string{sizeBytesAnnotation: "10737418240"})
			}
			contentspec := content.Object["spec"].(map[string]any)
			contentspec["source"] = backend
			contentspec["driver"] = tc.driver
			contentspec["sourceVolumeMode"] = tc.mode
			contentspec["volumeSnapshotRef"].(map[string]any)["uid"] = string(csi.GetUID())
			createObject(t, c, content)
			content.Object["status"] = map[string]any{"readyToUse": true, "snapshotHandle": importedHandle}
			must(t, c.Status().Update(ctx, content))
			requests := snapshotContentOwner(ctx, content)
			if len(requests) != 1 || requests[0].Name != snapshot.Name || requests[0].Namespace != ns {
				t.Fatalf("unexpected content event requests %+v", requests)
			}

			r := &VolumeSnapshotReconciler{Client: c}
			reconcileSnapshot(t, r, ns)
			observed := requireSnapshotReason(t, c, ns, "Ready")
			if observed.Status.SizeBytes != 10<<30 {
				t.Fatalf("restored size %d", observed.Status.SizeBytes)
			}
			newVolume(t, c, ns, "restore", func(volume *v1alpha1.Volume) {
				volume.Spec.Source = &v1alpha1.VolumeDataSource{SnapshotRef: snapshot.Name}
				volume.Spec.StorageClass = tc.name + "-class"
			})
			reconcileVolume(t, c, ns, "restore")
			clone := requirePresent(t, c, dataVolumeGVK, ns, "restore")
			input, _, _ := unstructured.NestedStringMap(clone.Object, "spec", "source", "snapshot")
			size, _, _ := unstructured.NestedString(clone.Object, "spec", "storage", "resources", "requests", "storage")
			class, _, _ := unstructured.NestedString(clone.Object, "spec", "storage", "storageClassName")
			if !reflect.DeepEqual(input, map[string]string{"name": snapshot.Name, "namespace": ns}) || size != "10Gi" || class != tc.name+"-class" {
				t.Fatalf("restore specification %+v", clone.Object["spec"])
			}
			if tc.name == "static" {
				content.SetAnnotations(nil)
				must(t, c.Update(ctx, content))
				reconcileSnapshot(t, r, ns)
				requireSnapshotReason(t, c, ns, ReasonSourceNotReady)
				must(t, unstructured.SetNestedField(content.Object, int64(10<<30), "status", "restoreSize"))
				must(t, c.Status().Update(ctx, content))
				reconcileSnapshot(t, r, ns)
				requireSnapshotReason(t, c, ns, "Ready")
			}

			csi = requirePresent(t, c, csiSnapshotGVK, ns, snapshot.Name)
			refs := csi.GetOwnerReferences()
			refs[0].UID = types.UID("previous-owner")
			csi.SetOwnerReferences(refs)
			must(t, c.Update(ctx, csi))
			reconcileSnapshot(t, r, ns)
			observed = requireSnapshotReason(t, c, ns, "VolumeNotFound")
			if observed.Status.Phase == v1alpha1.VolumeSnapshotPhaseReady {
				t.Fatal("a replacement owner retained restore access")
			}
			newVolume(t, c, ns, "after-replacement", func(volume *v1alpha1.Volume) {
				volume.Spec.Source = &v1alpha1.VolumeDataSource{SnapshotRef: snapshot.Name}
			})
			requireVolumeReason(t, reconcileVolume(t, c, ns, "after-replacement"), ReasonSourceNotReady)
			csi = requirePresent(t, c, csiSnapshotGVK, ns, snapshot.Name)
			csi.SetOwnerReferences([]metav1.OwnerReference{controllerRef("VolumeSnapshot", snapshot)})
			must(t, c.Update(ctx, csi))
			content = requirePresent(t, c, csiSnapshotContentGVK, "", contentName)
			must(t, unstructured.SetNestedField(content.Object, "previous-snapshot", "spec", "volumeSnapshotRef", "uid"))
			must(t, c.Update(ctx, content))
			reconcileSnapshot(t, r, ns)
			requireSnapshotReason(t, c, ns, "VolumeNotFound")
		})
	}
}
