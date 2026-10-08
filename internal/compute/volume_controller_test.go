package compute

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func reconcileVolume(t *testing.T, c client.Client, ns, name string) v1alpha1.Volume {
	t.Helper()
	r := &VolumeReconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: ns, Name: name}})
	must(t, err)
	var volume v1alpha1.Volume
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &volume); err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return volume
}

func volumeReady(volume v1alpha1.Volume) *metav1.Condition {
	return meta.FindStatusCondition(volume.Status.Conditions, v1alpha1.ConditionReady)
}

func requireVolumeReason(t *testing.T, volume v1alpha1.Volume, want string) {
	t.Helper()
	if cond := volumeReady(volume); cond == nil || cond.Reason != want {
		t.Fatalf("Ready %+v, want reason %s", cond, want)
	}
}

func newVolume(t *testing.T, c client.Client, ns, name string, mutate func(*v1alpha1.Volume)) {
	t.Helper()
	volume := v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: v1alpha1.VolumeSpec{SizeGi: 10}}
	mutate(&volume)
	createObject(t, c, &volume)
}

func setDataVolumePhase(t *testing.T, c client.Client, ns, name, phase string) {
	t.Helper()
	claim := requirePresent(t, c, dataVolumeGVK, ns, name)
	claim.Object["status"] = map[string]any{"phase": phase}
	must(t, c.Status().Update(context.Background(), claim))
}

func TestVolumeRendersADataVolume(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	newVolume(t, c, ns, "data1", func(v *v1alpha1.Volume) {})
	newVolume(t, c, ns, "data2", func(v *v1alpha1.Volume) { v.Spec.StorageClass = "block-retain" })
	volume := reconcileVolume(t, c, ns, "data1")
	reconcileVolume(t, c, ns, "data2")
	claim := requirePresent(t, c, dataVolumeGVK, ns, "data1")
	size, _, _ := unstructured.NestedString(claim.Object, "spec", "storage", "resources", "requests", "storage")
	_, hasClass, _ := unstructured.NestedString(claim.Object, "spec", "storage", "storageClassName")
	_, blank, _ := unstructured.NestedMap(claim.Object, "spec", "source", "blank")
	if size != "10Gi" || hasClass || !blank {
		t.Fatalf("spec %+v", claim.Object["spec"])
	}
	if claim.GetAnnotations()["cdi.kubevirt.io/allowClaimAdoption"] != "true" || !reflect.DeepEqual(claim.GetLabels(), v1alpha1.TenantLabels("Volume", "data1", "acme", "web")) {
		t.Fatalf("metadata %+v", claim.Object["metadata"])
	}
	refs := claim.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "Volume" || refs[0].UID != volume.UID || refs[0].Controller == nil || !*refs[0].Controller {
		t.Fatalf("owner %+v", refs)
	}
	if !containsString(volume.Finalizers, FinalizerVolumeInUse) {
		t.Fatalf("finalizers %+v", volume.Finalizers)
	}
	class, _, _ := unstructured.NestedString(requirePresent(t, c, dataVolumeGVK, ns, "data2").Object, "spec", "storage", "storageClassName")
	if class != "block-retain" {
		t.Fatalf("class %q", class)
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func readySnapshot(t *testing.T, c client.Client, ns, name string, sizeBytes int64) {
	t.Helper()
	snapshot := &v1alpha1.VolumeSnapshot{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: v1alpha1.VolumeSnapshotSpec{VolumeRef: "data1"}}
	createObject(t, c, snapshot)
	snapshot.Status.Phase, snapshot.Status.SizeBytes = v1alpha1.VolumeSnapshotPhaseReady, sizeBytes
	must(t, c.Status().Update(context.Background(), snapshot))
}

func TestVolumeSources(t *testing.T) {
	c, _ := startEnv(t)
	seedCatalog(t, c)
	ns := uniqueNamespace(t, c)
	readySnapshot(t, c, ns, "snap1", 5<<30)
	createPVC(t, c, ns, "base", 5, "block")
	newVolume(t, c, ns, "from-image", func(v *v1alpha1.Volume) {
		v.Spec.Source = &v1alpha1.VolumeDataSource{Image: &v1alpha1.ImageRef{Name: "ubuntu-24.04", Scope: v1alpha1.ImageScopeCluster}}
	})
	newVolume(t, c, ns, "from-snapshot", func(v *v1alpha1.Volume) { v.Spec.Source = &v1alpha1.VolumeDataSource{SnapshotRef: "snap1"} })
	newVolume(t, c, ns, "from-volume", func(v *v1alpha1.Volume) { v.Spec.Source = &v1alpha1.VolumeDataSource{VolumeRef: "base"} })
	for _, name := range []string{"from-image", "from-snapshot", "from-volume"} {
		reconcileVolume(t, c, ns, name)
	}
	ref, _, _ := unstructured.NestedMap(requirePresent(t, c, dataVolumeGVK, ns, "from-image").Object, "spec", "sourceRef")
	if !reflect.DeepEqual(ref, map[string]any{"kind": "DataSource", "name": "ubuntu-24-04", "namespace": "bedrock-images"}) {
		t.Fatalf("sourceRef %+v", ref)
	}
	snapshot, _, _ := unstructured.NestedMap(requirePresent(t, c, dataVolumeGVK, ns, "from-snapshot").Object, "spec", "source", "snapshot")
	if !reflect.DeepEqual(snapshot, map[string]any{"namespace": ns, "name": "snap1"}) {
		t.Fatalf("snapshot %+v", snapshot)
	}
	pvc, _, _ := unstructured.NestedMap(requirePresent(t, c, dataVolumeGVK, ns, "from-volume").Object, "spec", "source", "pvc")
	if !reflect.DeepEqual(pvc, map[string]any{"namespace": ns, "name": "base"}) {
		t.Fatalf("pvc %+v", pvc)
	}
}

func TestVolumeSourcesStayInNamespace(t *testing.T) {
	volume := v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Namespace: "acme-web", Name: "data1"}, Spec: v1alpha1.VolumeSpec{SizeGi: 10}}
	labels := v1alpha1.TenantLabels("Volume", "data1", "acme", "web")
	cases := map[string]struct {
		source ClaimSource
		path   []string
	}{
		"snapshot": {ClaimSource{SnapshotName: "snap1"}, []string{"spec", "source", "snapshot", "namespace"}},
		"volume":   {ClaimSource{VolumeName: "base"}, []string{"spec", "source", "pvc", "namespace"}},
		"image":    {ClaimSource{Image: &ResolvedImage{Namespace: "acme-web", DataSource: "x"}}, []string{"spec", "sourceRef", "namespace"}},
	}
	for name, tc := range cases {
		claim, err := VolumeClaim(volume, labels, tc.source)
		must(t, err)
		if got, _, _ := unstructured.NestedString(claim.Object, tc.path...); got != "acme-web" || claim.GetNamespace() != "acme-web" {
			t.Errorf("%s namespace %q", name, got)
		}
	}
	if _, err := VolumeClaim(volume, labels, ClaimSource{SnapshotName: "snap1", VolumeName: "base"}); err == nil {
		t.Error("two sources must be refused")
	}
}

func TestVolumeWaitsForSource(t *testing.T) {
	c, _ := startEnv(t)
	seedCatalog(t, c)
	ns := uniqueNamespace(t, c)
	createObject(t, c, &v1alpha1.VolumeSnapshot{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "pending"}, Spec: v1alpha1.VolumeSnapshotSpec{VolumeRef: "data1"}})
	cases := map[string]*v1alpha1.VolumeDataSource{
		"pending-snapshot": {SnapshotRef: "pending"},
		"missing-clone":    {VolumeRef: "ghost"},
		"missing-image":    {Image: &v1alpha1.ImageRef{Name: "ghost-image", Scope: v1alpha1.ImageScopeCluster}},
		"small":            {Image: &v1alpha1.ImageRef{Name: "ubuntu-24.04", Scope: v1alpha1.ImageScopeCluster}},
	}
	for name, source := range cases {
		newVolume(t, c, ns, name, func(v *v1alpha1.Volume) {
			v.Spec.Source = source
			if name == "small" {
				v.Spec.SizeGi = 1
			}
		})
		requireVolumeReason(t, reconcileVolume(t, c, ns, name), "SourceNotReady")
		requireAbsent(t, c, dataVolumeGVK, ns, name)
	}
}

func TestVolumeDoesNotRewriteTheDataVolume(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	newVolume(t, c, ns, "data1", func(v *v1alpha1.Volume) {})
	volume := reconcileVolume(t, c, ns, "data1")
	before := requirePresent(t, c, dataVolumeGVK, ns, "data1")
	volume.Spec.Bus = v1alpha1.DiskBusSCSI
	must(t, c.Update(context.Background(), &volume))
	reconcileVolume(t, c, ns, "data1")
	after := requirePresent(t, c, dataVolumeGVK, ns, "data1")
	if !reflect.DeepEqual(before.Object["spec"], after.Object["spec"]) || before.GetResourceVersion() != after.GetResourceVersion() {
		t.Fatalf("the DataVolume changed: %+v", after.Object["spec"])
	}
}

func TestVolumeExpandsThePVC(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	newVolume(t, c, ns, "data1", func(v *v1alpha1.Volume) {})
	createPVC(t, c, ns, "data1", 10, "block")
	volume := reconcileVolume(t, c, ns, "data1")
	setDataVolumePhase(t, c, ns, "data1", "Succeeded")
	volume.Spec.SizeGi = 20
	must(t, c.Update(context.Background(), &volume))
	volume = reconcileVolume(t, c, ns, "data1")
	var pvc corev1.PersistentVolumeClaim
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &pvc))
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != "20Gi" {
		t.Fatalf("request %s", got)
	}
	if volume.Status.Phase != v1alpha1.VolumePhaseResizing {
		t.Fatalf("phase %s", volume.Status.Phase)
	}
	pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}
	must(t, c.Status().Update(context.Background(), &pvc))
	volume = reconcileVolume(t, c, ns, "data1")
	if volume.Status.Phase != v1alpha1.VolumePhaseAvailable || volume.Status.ActualSizeGi != 20 {
		t.Fatalf("status %+v", volume.Status)
	}
}

func TestVolumeShrinkIsRefused(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	createPVC(t, c, ns, "data1", 20, "block")
	newVolume(t, c, ns, "data1", func(v *v1alpha1.Volume) {})
	requireVolumeReason(t, reconcileVolume(t, c, ns, "data1"), "ShrinkRefused")
	var pvc corev1.PersistentVolumeClaim
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &pvc))
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != "20Gi" {
		t.Fatalf("request %s", got)
	}
}

func TestVolumeExpansionNeedsExpandableClass(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	ensureStorageClass(t, c, "fixed", "rook-ceph.rbd.csi.ceph.com", false)
	createPVC(t, c, ns, "data1", 10, "fixed")
	newVolume(t, c, ns, "data1", func(v *v1alpha1.Volume) { v.Spec.SizeGi = 20 })
	requireVolumeReason(t, reconcileVolume(t, c, ns, "data1"), "ExpansionNotSupported")
	var pvc corev1.PersistentVolumeClaim
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &pvc))
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != "10Gi" {
		t.Fatalf("request %s", got)
	}
}

func TestVolumePhaseOf(t *testing.T) {
	cases := []struct {
		phase, attached string
		capacity, size  int32
		want            string
	}{
		{"", "", 0, 10, "Pending"}, {"Succeeded", "", 10, 10, "Available"}, {"Succeeded", "vm", 10, 10, "InUse"},
		{"WaitForFirstConsumer", "", 0, 10, "Available"}, {"WaitForFirstConsumer", "vm", 0, 10, "InUse"},
		{"PendingPopulation", "", 0, 10, "Available"}, {"PendingPopulation", "vm", 0, 10, "InUse"},
		{"ImportInProgress", "", 0, 10, "Pending"}, {"CloneInProgress", "", 0, 10, "Pending"},
		{"CloneScheduled", "", 0, 10, "Pending"}, {"Pending", "", 0, 10, "Pending"},
		{"Failed", "", 0, 10, "Failed"}, {"Succeeded", "", 10, 20, "Resizing"},
	}
	for _, tc := range cases {
		if got := VolumePhaseOf(tc.phase, tc.attached, tc.capacity, tc.size); got != tc.want {
			t.Errorf("%+v gave %s", tc, got)
		}
	}
}

func TestVolumeAttachedTo(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	reconcileVolume(t, c, ns, "data1")
	setDataVolumePhase(t, c, ns, "data1", "Succeeded")
	createVM(t, c, ns, "older", noMutation)
	createVM(t, c, ns, "younger", noMutation)
	volume := reconcileVolume(t, c, ns, "data1")
	if volume.Status.AttachedTo != "older" || volume.Status.Phase != v1alpha1.VolumePhaseInUse {
		t.Fatalf("status %+v", volume.Status)
	}
}

func TestVolumeDeleteWaitsForTheVM(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	reconcileVolume(t, c, ns, "data1")
	createVM(t, c, ns, "web-1", noMutation)
	must(t, c.Delete(context.Background(), &v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "data1"}}))
	volume := reconcileVolume(t, c, ns, "data1")
	if volume.DeletionTimestamp.IsZero() || !containsString(volume.Finalizers, FinalizerVolumeInUse) {
		t.Fatalf("volume %+v", volume.ObjectMeta)
	}
	requireVolumeReason(t, volume, "InUse")
	vm := getVM(t, c, ns, "web-1")
	vm.Spec.Disks = nil
	must(t, c.Update(context.Background(), &vm))
	reconcileVolume(t, c, ns, "data1")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &v1alpha1.Volume{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the volume must be gone: %v", err)
	}
}

func TestVolumeRestoreHoldLeavesTheClaimUntouched(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	newVolume(t, c, ns, "data1", func(v *v1alpha1.Volume) {})
	volume := reconcileVolume(t, c, ns, "data1")
	createPVC(t, c, ns, "data1", 10, "block")
	var pvc corev1.PersistentVolumeClaim
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &pvc))
	claimVersion := requirePresent(t, c, dataVolumeGVK, ns, "data1").GetResourceVersion()
	volume.Annotations = map[string]string{AnnotationRestoreHold: "restore-1"}
	volume.Spec.SizeGi = 20
	must(t, c.Update(context.Background(), &volume))
	volume = reconcileVolume(t, c, ns, "data1")
	requireVolumeReason(t, volume, "RestoreInProgress")
	var after corev1.PersistentVolumeClaim
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &after))
	if after.ResourceVersion != pvc.ResourceVersion || requirePresent(t, c, dataVolumeGVK, ns, "data1").GetResourceVersion() != claimVersion {
		t.Fatal("the claims changed during the hold")
	}
}

func TestVolumeClaimsCarryTheTenantLabels(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	newVolume(t, c, ns, "data1", func(v *v1alpha1.Volume) {})
	reconcileVolume(t, c, ns, "data1")
	createPVC(t, c, ns, "data1", 10, "block")
	reconcileVolume(t, c, ns, "data1")
	want := v1alpha1.TenantLabels("Volume", "data1", "acme", "web")
	var pvc corev1.PersistentVolumeClaim
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &pvc))
	if !reflect.DeepEqual(pvc.Labels, want) || !reflect.DeepEqual(requirePresent(t, c, dataVolumeGVK, ns, "data1").GetLabels(), want) {
		t.Fatalf("pvc labels %+v", pvc.Labels)
	}
}

func TestVolumeOutsideProjectNamespaceIsRefused(t *testing.T) {
	c, _ := startEnv(t)
	ns := "plain-" + uniqueNamespaceSuffix()
	createObject(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	newVolume(t, c, ns, "data1", func(v *v1alpha1.Volume) {})
	requireVolumeReason(t, reconcileVolume(t, c, ns, "data1"), "NotInProject")
	requireAbsent(t, c, dataVolumeGVK, ns, "data1")
}

func TestActualSizeGi(t *testing.T) {
	ten := resource.MustParse("10Gi")
	plusOne := ten.DeepCopy()
	plusOne.Add(*resource.NewQuantity(1, resource.BinarySI))
	if ActualSizeGi(ten) != 10 || ActualSizeGi(plusOne) != 10 || ActualSizeGi(resource.Quantity{}) != 0 {
		t.Fatal("sizes differ")
	}
}

func TestVolumeDeletionIgnoresChangedNamespaceLabels(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	newVolume(t, c, ns, "data1", func(v *v1alpha1.Volume) {})
	reconcileVolume(t, c, ns, "data1")
	var namespace corev1.Namespace
	must(t, c.Get(context.Background(), client.ObjectKey{Name: ns}, &namespace))
	namespace.Labels = nil
	must(t, c.Update(context.Background(), &namespace))
	must(t, c.Delete(context.Background(), &v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "data1"}}))
	reconcileVolume(t, c, ns, "data1")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &v1alpha1.Volume{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the volume must be gone: %v", err)
	}
}
