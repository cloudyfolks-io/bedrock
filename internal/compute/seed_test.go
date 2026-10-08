package compute

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var fixedNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func readyConditions(status metav1.ConditionStatus) []metav1.Condition {
	return []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: status, Reason: "Test", LastTransitionTime: metav1.Now()}}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func createObject(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	must(t, c.Create(context.Background(), obj))
}

func createSubnet(t *testing.T, c client.Client, namespace, name string, ready metav1.ConditionStatus) {
	t.Helper()
	subnet := &v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.SubnetSpec{NetworkRef: "acme", CIDR: "10.100.0.0/24"},
	}
	createObject(t, c, subnet)
	subnet.Status.Conditions = readyConditions(ready)
	must(t, c.Status().Update(context.Background(), subnet))
}

func createSecurityGroup(t *testing.T, c client.Client, namespace, name string, ready metav1.ConditionStatus) {
	t.Helper()
	group := &v1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	createObject(t, c, group)
	group.Status.Conditions = readyConditions(ready)
	must(t, c.Status().Update(context.Background(), group))
}

func createSSHKey(t *testing.T, c client.Client, namespace, name string, ready metav1.ConditionStatus) {
	t.Helper()
	key := &v1alpha1.SSHKey{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.SSHKeySpec{PublicKey: ed25519Key(t) + " " + name + "@laptop"},
	}
	createObject(t, c, key)
	key.Status.Conditions = readyConditions(ready)
	must(t, c.Status().Update(context.Background(), key))
}

func createVolume(t *testing.T, c client.Client, namespace, name string) *v1alpha1.Volume {
	t.Helper()
	volume := &v1alpha1.Volume{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.VolumeSpec{SizeGi: 10},
	}
	createObject(t, c, volume)
	return volume
}

func ensureClusterImage(t *testing.T, c client.Client, name, imageType, dataSource, phase string, sizeBytes int64, preference string) {
	t.Helper()
	ctx := context.Background()
	image := &v1alpha1.ClusterImage{}
	err := c.Get(ctx, client.ObjectKey{Name: name}, image)
	if apierrors.IsNotFound(err) {
		image = &v1alpha1.ClusterImage{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.ImageSpec{
				Type:   imageType,
				OS:     &v1alpha1.ImageOS{Preference: preference},
				Source: v1alpha1.ImageSource{Registry: &v1alpha1.ImageRegistrySource{Image: "registry.test/" + name}},
			},
		}
		must(t, c.Create(ctx, image))
	} else {
		must(t, err)
	}
	image.Status.Phase, image.Status.DataSource, image.Status.SizeBytes = phase, dataSource, sizeBytes
	must(t, c.Status().Update(ctx, image))
}

func ensureUnstructured(t *testing.T, c client.Client, gvk schema.GroupVersionKind, name string, spec map[string]any) {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": name}, "spec": spec}}
	obj.SetGroupVersionKind(gvk)
	if err := c.Create(context.Background(), obj); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

func seedCatalog(t *testing.T, c client.Client) {
	t.Helper()
	ensureClusterImage(t, c, "ubuntu-24.04", v1alpha1.ImageTypeDisk, "ubuntu-24-04", v1alpha1.ImagePhaseReady, 3<<30, "ubuntu")
	ensureClusterImage(t, c, "install-iso", v1alpha1.ImageTypeISO, "install-iso", v1alpha1.ImagePhaseReady, 1<<30, "")
	ensureUnstructured(t, c, instanceTypeGVK, "u1.medium",
		map[string]any{"cpu": map[string]any{"guest": int64(1)}, "memory": map[string]any{"guest": "4Gi"}})
	ensureUnstructured(t, c, preferenceGVK, "ubuntu", map[string]any{})
	ensureUnstructured(t, c, preferenceGVK, "windows.2k22", map[string]any{})
}

func seedPlatform(t *testing.T, c client.Client) string {
	t.Helper()
	seedCatalog(t, c)
	ns := uniqueNamespace(t, c)
	createSubnet(t, c, ns, "default", metav1.ConditionTrue)
	createSecurityGroup(t, c, ns, "web", metav1.ConditionTrue)
	ensureUnstructured(t, c, fabricGroupGVK, DefaultFabricGroup(ns), map[string]any{})
	createSSHKey(t, c, ns, "alice", metav1.ConditionTrue)
	createVolume(t, c, ns, "data1")
	return ns
}

func newVMReconciler(c client.Client) *VirtualMachineReconciler {
	return &VirtualMachineReconciler{
		Client: c,
		GuestInfo: func(context.Context, string, string) (v1alpha1.VMGuestAgent, error) {
			return v1alpha1.VMGuestAgent{Version: "5.0.0", OS: "Ubuntu 24.04 LTS", Hostname: "web-1"}, nil
		},
		Now: func() time.Time { return fixedNow },
	}
}

func reconcileVM(t *testing.T, r *VirtualMachineReconciler, namespace, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}})
	must(t, err)
	return result
}

func createVM(t *testing.T, c client.Client, namespace, name string, mutate func(*v1alpha1.VirtualMachine)) {
	t.Helper()
	vm := fixtureVM(namespace, name)
	mutate(&vm)
	createObject(t, c, &vm)
}

func getVM(t *testing.T, c client.Client, namespace, name string) v1alpha1.VirtualMachine {
	t.Helper()
	var vm v1alpha1.VirtualMachine
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &vm))
	return vm
}

func readyOf(vm v1alpha1.VirtualMachine) *metav1.Condition {
	return meta.FindStatusCondition(vm.Status.Conditions, v1alpha1.ConditionReady)
}

func requireReason(t *testing.T, vm v1alpha1.VirtualMachine, want string) {
	t.Helper()
	cond := readyOf(vm)
	if cond == nil || cond.Reason != want {
		t.Fatalf("Ready condition %+v, want reason %s", cond, want)
	}
}

func getUnstructured(c client.Client, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return obj, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj)
}

func requireAbsent(t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) {
	t.Helper()
	if _, err := getUnstructured(c, gvk, namespace, name); !apierrors.IsNotFound(err) {
		t.Fatalf("%s %s must not exist: %v", gvk.Kind, name, err)
	}
}

func requirePresent(t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	obj, err := getUnstructured(c, gvk, namespace, name)
	must(t, err)
	return obj
}

func ensureStorageClass(t *testing.T, c client.Client, name, provisioner string, expandable bool) {
	t.Helper()
	class := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Provisioner: provisioner, AllowVolumeExpansion: &expandable}
	if err := c.Create(context.Background(), class); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

func createPVC(t *testing.T, c client.Client, namespace, name string, gi int64, class string) *corev1.PersistentVolumeClaim {
	t.Helper()
	ensureStorageClass(t, c, class, "rook-ceph.rbd.csi.ceph.com", true)
	quantity := *resource.NewQuantity(gi<<30, resource.BinarySI)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: quantity}},
			StorageClassName: &class,
			VolumeName:       "pv-" + namespace + "-" + name,
		},
	}
	createObject(t, c, pvc)
	pvc.Status.Phase = corev1.ClaimBound
	pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: quantity}
	must(t, c.Status().Update(context.Background(), pvc))
	return pvc
}
