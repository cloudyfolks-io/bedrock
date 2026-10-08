package compute

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	rbdProvisioner    = "rook-ceph.rbd.csi.ceph.com"
	cephfsProvisioner = "rook-ceph.cephfs.csi.ceph.com"
)

func ensureSnapshotClass(t *testing.T, c client.Client, name, driver string, annotations map[string]string) {
	t.Helper()
	obj := emptyOf(csiSnapshotClassGVK)
	obj.SetName(name)
	obj.SetAnnotations(annotations)
	obj.Object["driver"] = driver
	obj.Object["deletionPolicy"] = "Delete"
	if err := c.Create(context.Background(), obj); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

func snapshotFixture(t *testing.T, c client.Client, class, provisioner string, retention *metav1.Duration) (string, *VolumeSnapshotReconciler) {
	t.Helper()
	ensureStorageClass(t, c, class, provisioner, true)
	ensureSnapshotClass(t, c, "block", rbdProvisioner, nil)
	ns := uniqueNamespace(t, c)
	createVolume(t, c, ns, "data1")
	createPVC(t, c, ns, "data1", 10, class)
	createObject(t, c, &v1alpha1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "snap1"},
		Spec:       v1alpha1.VolumeSnapshotSpec{VolumeRef: "data1", Retention: retention},
	})
	return ns, &VolumeSnapshotReconciler{Client: c, Now: func() time.Time { return fixedNow }}
}

func reconcileSnapshot(t *testing.T, r *VolumeSnapshotReconciler, ns string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: ns, Name: "snap1"}})
	must(t, err)
	return result
}

func getSnapshot(t *testing.T, c client.Client, ns string) (v1alpha1.VolumeSnapshot, error) {
	t.Helper()
	var snapshot v1alpha1.VolumeSnapshot
	err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "snap1"}, &snapshot)
	return snapshot, err
}

func requireSnapshotReason(t *testing.T, c client.Client, ns, want string) v1alpha1.VolumeSnapshot {
	t.Helper()
	snapshot, err := getSnapshot(t, c, ns)
	must(t, err)
	cond := meta.FindStatusCondition(snapshot.Status.Conditions, v1alpha1.ConditionReady)
	if cond == nil || cond.Reason != want {
		t.Fatalf("Ready %+v, want %s", cond, want)
	}
	return snapshot
}

func TestVolumeSnapshotRendersCSISnapshot(t *testing.T) {
	c, _ := startEnv(t)
	ns, r := snapshotFixture(t, c, "block", rbdProvisioner, nil)
	reconcileSnapshot(t, r, ns)
	csi := requirePresent(t, c, csiSnapshotGVK, ns, "snap1")
	claim, _, _ := unstructured.NestedString(csi.Object, "spec", "source", "persistentVolumeClaimName")
	class, _, _ := unstructured.NestedString(csi.Object, "spec", "volumeSnapshotClassName")
	snapshot, _ := getSnapshot(t, c, ns)
	refs := csi.GetOwnerReferences()
	if claim != "data1" || class != "block" || len(refs) != 1 || refs[0].UID != snapshot.UID || refs[0].Controller == nil || !*refs[0].Controller {
		t.Fatalf("snapshot %+v", csi.Object)
	}
	if csi.GetLabels()[v1alpha1.LabelKind] != "VolumeSnapshot" || csi.GetLabels()[v1alpha1.LabelProject] != "web" {
		t.Fatalf("labels %+v", csi.GetLabels())
	}
}

func TestSnapshotClassFor(t *testing.T) {
	class := func(name, driver string, annotations map[string]string) unstructured.Unstructured {
		obj := unstructured.Unstructured{Object: map[string]any{"driver": driver}}
		obj.SetName(name)
		obj.SetAnnotations(annotations)
		return obj
	}
	classes := []unstructured.Unstructured{
		class("z-rbd", rbdProvisioner, nil), class("a-rbd", rbdProvisioner, nil), class("fs", cephfsProvisioner, nil),
	}
	if got, err := SnapshotClassFor(cephfsProvisioner, classes); err != nil || got != "fs" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := SnapshotClassFor("other", classes); err == nil {
		t.Fatal("no match must fail")
	}
	if got, _ := SnapshotClassFor(rbdProvisioner, classes); got != "a-rbd" {
		t.Fatalf("first by name: %q", got)
	}
	classes[0] = class("z-rbd", rbdProvisioner, map[string]string{annotationDefaultSnapshotClass: "true"})
	if got, _ := SnapshotClassFor(rbdProvisioner, classes); got != "z-rbd" {
		t.Fatalf("default class: %q", got)
	}
}

func TestVolumeSnapshotNoClass(t *testing.T) {
	c, _ := startEnv(t)
	ns, r := snapshotFixture(t, c, "filesystem", cephfsProvisioner, nil)
	reconcileSnapshot(t, r, ns)
	requireSnapshotReason(t, c, ns, "NoSnapshotClass")
	requireAbsent(t, c, csiSnapshotGVK, ns, "snap1")
}

func TestVolumeSnapshotVolumeNotFound(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	createObject(t, c, &v1alpha1.VolumeSnapshot{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "snap1"}, Spec: v1alpha1.VolumeSnapshotSpec{VolumeRef: "ghost"}})
	reconcileSnapshot(t, &VolumeSnapshotReconciler{Client: c}, ns)
	requireSnapshotReason(t, c, ns, "VolumeNotFound")
}

func TestVolumeSnapshotStatus(t *testing.T) {
	c, _ := startEnv(t)
	ns, r := snapshotFixture(t, c, "block", rbdProvisioner, nil)
	reconcileSnapshot(t, r, ns)
	if snapshot := requireSnapshotReason(t, c, ns, "Pending"); snapshot.Status.Phase != "Pending" {
		t.Fatalf("status %+v", snapshot.Status)
	}
	csi := requirePresent(t, c, csiSnapshotGVK, ns, "snap1")
	csi.Object["status"] = map[string]any{"readyToUse": true, "restoreSize": "10Gi"}
	must(t, c.Status().Update(context.Background(), csi))
	reconcileSnapshot(t, r, ns)
	snapshot := requireSnapshotReason(t, c, ns, "Ready")
	if snapshot.Status.Phase != "Ready" || snapshot.Status.SizeBytes != 10<<30 {
		t.Fatalf("status %+v", snapshot.Status)
	}
	csi = requirePresent(t, c, csiSnapshotGVK, ns, "snap1")
	csi.Object["status"] = map[string]any{"readyToUse": false, "error": map[string]any{"message": "driver down"}}
	must(t, c.Status().Update(context.Background(), csi))
	reconcileSnapshot(t, r, ns)
	snapshot = requireSnapshotReason(t, c, ns, "Failed")
	if snapshot.Status.Phase != "Failed" || meta.FindStatusCondition(snapshot.Status.Conditions, v1alpha1.ConditionReady).Message != "the storage driver could not create the snapshot" {
		t.Fatalf("status %+v", snapshot.Status)
	}
}

func TestVolumeSnapshotRetention(t *testing.T) {
	c, _ := startEnv(t)
	ns, r := snapshotFixture(t, c, "block", rbdProvisioner, &metav1.Duration{Duration: time.Hour})
	snapshot, err := getSnapshot(t, c, ns)
	must(t, err)
	created := snapshot.CreationTimestamp.Time
	r.Now = func() time.Time { return created.Add(30 * time.Minute) }
	if got := reconcileSnapshot(t, r, ns).RequeueAfter; got != 30*time.Minute {
		t.Fatalf("requeue %s", got)
	}
	if _, err := getSnapshot(t, c, ns); err != nil {
		t.Fatal(err)
	}
	r.Now = func() time.Time { return created.Add(61 * time.Minute) }
	reconcileSnapshot(t, r, ns)
	if _, err := getSnapshot(t, c, ns); !apierrors.IsNotFound(err) {
		t.Fatalf("the snapshot must be gone: %v", err)
	}
}

func TestVolumeSnapshotWithoutRetentionStays(t *testing.T) {
	c, _ := startEnv(t)
	for _, retention := range []*metav1.Duration{nil, {Duration: 0}} {
		ns, r := snapshotFixture(t, c, "block", rbdProvisioner, retention)
		r.Now = func() time.Time { return fixedNow.Add(1000 * time.Hour) }
		if got := reconcileSnapshot(t, r, ns).RequeueAfter; got != 0 {
			t.Fatalf("requeue %s", got)
		}
		if _, err := getSnapshot(t, c, ns); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotExpiry(t *testing.T) {
	created := fixedNow
	if at, ok := SnapshotExpiry(created, &metav1.Duration{Duration: time.Hour}); !ok || !at.Equal(created.Add(time.Hour)) {
		t.Fatalf("%v %v", at, ok)
	}
	for _, retention := range []*metav1.Duration{nil, {Duration: 0}, {Duration: -time.Hour}} {
		if _, ok := SnapshotExpiry(created, retention); ok {
			t.Fatalf("%v must not expire", retention)
		}
	}
}

func TestVolumeSnapshotRefusesAForeignCSISnapshot(t *testing.T) {
	c, _ := startEnv(t)
	ns, r := snapshotFixture(t, c, "block", rbdProvisioner, nil)
	foreign := emptyOf(csiSnapshotGVK)
	foreign.SetNamespace(ns)
	foreign.SetName("snap1")
	foreign.Object["spec"] = map[string]any{"source": map[string]any{"persistentVolumeClaimName": "other"}}
	createObject(t, c, foreign)
	reconcileSnapshot(t, r, ns)
	requireSnapshotReason(t, c, ns, "NameConflict")
	csi := requirePresent(t, c, csiSnapshotGVK, ns, "snap1")
	if claim, _, _ := unstructured.NestedString(csi.Object, "spec", "source", "persistentVolumeClaimName"); claim != "other" {
		t.Fatalf("the snapshot changed: %+v", csi.Object)
	}
}
