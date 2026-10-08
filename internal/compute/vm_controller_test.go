package compute

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func noMutation(*v1alpha1.VirtualMachine) {}

func TestVMRendersUnderlyingObjects(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	vm := getVM(t, c, ns, "web-1")
	want := v1alpha1.TenantLabels("VirtualMachine", "web-1", "acme", "web")
	kubeVirt := requirePresent(t, c, kubeVirtVMGVK, ns, "web-1")
	root := requirePresent(t, c, dataVolumeGVK, ns, "web-1-root")
	var secret corev1.Secret
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web-1-cloudinit"}, &secret))
	for _, obj := range []client.Object{kubeVirt, root, &secret} {
		if !reflect.DeepEqual(obj.GetLabels(), want) {
			t.Errorf("%s labels %+v", obj.GetName(), obj.GetLabels())
		}
		refs := obj.GetOwnerReferences()
		if len(refs) != 1 || refs[0].UID != vm.UID || refs[0].Controller == nil || !*refs[0].Controller {
			t.Errorf("%s owner %+v", obj.GetName(), refs)
		}
	}
	managed := false
	for _, entry := range kubeVirt.GetManagedFields() {
		managed = managed || entry.Manager == "bedrock-compute"
	}
	if !managed {
		t.Error("the KubeVirt VM has no bedrock-compute field manager")
	}
	var alice v1alpha1.SSHKey
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "alice"}, &alice))
	normalized, _ := AuthorizedKey(alice.Spec.PublicKey)
	if !strings.Contains(string(secret.Data["userdata"]), normalized) {
		t.Errorf("userdata %q lacks the key", secret.Data["userdata"])
	}
	annotations, _, _ := unstructured.NestedStringMap(kubeVirt.Object, "spec", "template", "metadata", "annotations")
	labels, _, _ := unstructured.NestedStringMap(kubeVirt.Object, "spec", "template", "metadata", "labels")
	if annotations["default."+ns+".fabric.cloudyfolks.io/port_security"] != "true" || annotations["default."+ns+".fabric.cloudyfolks.io/security_groups"] != FabricName(ns, "web") || labels["sg.bedrock.cloudyfolks.io/web"] != "true" {
		t.Errorf("annotations %+v labels %+v", annotations, labels)
	}
	requireReason(t, vm, "Starting")
}

func TestVMOutsideProjectNamespaceIsRefused(t *testing.T) {
	c, _ := startEnv(t)
	ns := "plain-" + uniqueNamespaceSuffix()
	createObject(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	createVM(t, c, ns, "web-1", noMutation)
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	requireReason(t, getVM(t, c, ns, "web-1"), "NotInProject")
	requireAbsent(t, c, kubeVirtVMGVK, ns, "web-1")
	requireAbsent(t, c, dataVolumeGVK, ns, "web-1-root")
}

func TestVMRefusesInvalidSpec(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "scoped", func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Scheduling = &v1alpha1.VMScheduling{Affinity: &corev1.Affinity{PodAffinity: &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			TopologyKey: "kubernetes.io/hostname", NamespaceSelector: &metav1.LabelSelector{},
		}}}}}
	})
	createVM(t, c, ns, "script", func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.CloudInit = &v1alpha1.VMCloudInit{UserData: "#!/bin/bash\necho hi\n"}
	})
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "scoped")
	reconcileVM(t, r, ns, "script")
	requireReason(t, getVM(t, c, ns, "scoped"), "InvalidSpec")
	requireReason(t, getVM(t, c, ns, "script"), "InvalidUserData")
	for _, name := range []string{"scoped", "script"} {
		requireAbsent(t, c, kubeVirtVMGVK, ns, name)
		requireAbsent(t, c, dataVolumeGVK, ns, name+"-root")
	}
}

func TestVMNeverAttachesAnotherNamespace(t *testing.T) {
	c, _ := startEnv(t)
	other := seedPlatform(t, c)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	networks, _, _ := unstructured.NestedSlice(requirePresent(t, c, kubeVirtVMGVK, ns, "web-1").Object, "spec", "template", "spec", "networks")
	if name, _, _ := unstructured.NestedString(networks[0].(map[string]any), "multus", "networkName"); name != ns+"/default" {
		t.Fatalf("network name %q", name)
	}
	createVM(t, c, other, "lonely", noMutation)
	must(t, c.Delete(context.Background(), &v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Namespace: other, Name: "default"}}))
	reconcileVM(t, r, other, "lonely")
	requireReason(t, getVM(t, c, other, "lonely"), "SubnetNotReady")
	requireAbsent(t, c, kubeVirtVMGVK, other, "lonely")
}

func TestVMWaitsForImage(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := newVMReconciler(c)
	createVM(t, c, ns, "missing", func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Image.Name = "ghost-image"
		vm.Spec.Disks = nil
	})
	reconcileVM(t, r, ns, "missing")
	requireReason(t, getVM(t, c, ns, "missing"), "ImageNotFound")
	createVM(t, c, ns, "iso", func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Image.Name = "install-iso"
		vm.Spec.Disks = nil
	})
	reconcileVM(t, r, ns, "iso")
	requireReason(t, getVM(t, c, ns, "iso"), "ImageWrongType")
	ensureClusterImage(t, c, "ubuntu-24.04", v1alpha1.ImageTypeDisk, "ubuntu-24-04", v1alpha1.ImagePhaseImporting, 0, "ubuntu")
	createVM(t, c, ns, "web-1", noMutation)
	reconcileVM(t, r, ns, "web-1")
	requireReason(t, getVM(t, c, ns, "web-1"), "ImageNotReady")
	requireAbsent(t, c, kubeVirtVMGVK, ns, "web-1")
	ensureClusterImage(t, c, "ubuntu-24.04", v1alpha1.ImageTypeDisk, "ubuntu-24-04", v1alpha1.ImagePhaseReady, 3<<30, "ubuntu")
	reconcileVM(t, r, ns, "web-1")
	requirePresent(t, c, kubeVirtVMGVK, ns, "web-1")
}

func TestVMWaitsForSubnet(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	var subnet v1alpha1.Subnet
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "default"}, &subnet))
	subnet.Status.Conditions = readyConditions(metav1.ConditionFalse)
	must(t, c.Status().Update(context.Background(), &subnet))
	createVM(t, c, ns, "web-1", noMutation)
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	vm := getVM(t, c, ns, "web-1")
	requireReason(t, vm, "SubnetNotReady")
	if network := conditionOf(vm, ConditionNetworkReady); network == nil || network.Status != metav1.ConditionFalse {
		t.Fatalf("network condition %+v", network)
	}
	requireAbsent(t, c, kubeVirtVMGVK, ns, "web-1")
	requireAbsent(t, c, dataVolumeGVK, ns, "web-1-root")
}

func conditionOf(vm v1alpha1.VirtualMachine, name string) *metav1.Condition {
	for index := range vm.Status.Conditions {
		if vm.Status.Conditions[index].Type == name {
			return &vm.Status.Conditions[index]
		}
	}
	return nil
}

func TestVMWaitsForSecurityGroup(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SecurityGroups = []string{"ghost"} })
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	requireReason(t, getVM(t, c, ns, "web-1"), "SecurityGroupNotReady")
	requireAbsent(t, c, kubeVirtVMGVK, ns, "web-1")
	createSecurityGroup(t, c, ns, "ghost", metav1.ConditionTrue)
	reconcileVM(t, r, ns, "web-1")
	requirePresent(t, c, kubeVirtVMGVK, ns, "web-1")
}

func TestVMWaitsForSSHKey(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createSSHKey(t, c, ns, "pending", metav1.ConditionFalse)
	r := newVMReconciler(c)
	for _, key := range []string{"absent", "pending"} {
		name := "vm-" + key
		createVM(t, c, ns, name, func(vm *v1alpha1.VirtualMachine) { vm.Spec.SSHKeys = []string{key} })
		reconcileVM(t, r, ns, name)
		requireReason(t, getVM(t, c, ns, name), "SSHKeyNotReady")
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name + "-cloudinit"}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
			t.Fatalf("no Secret expected: %v", err)
		}
	}
}

func TestVMRefusesVolumeInUse(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := newVMReconciler(c)
	createVM(t, c, ns, "a", noMutation)
	createVM(t, c, ns, "b", noMutation)
	reconcileVM(t, r, ns, "a")
	reconcileVM(t, r, ns, "b")
	requirePresent(t, c, kubeVirtVMGVK, ns, "a")
	requireReason(t, getVM(t, c, ns, "b"), "VolumeInUse")
	requireAbsent(t, c, kubeVirtVMGVK, ns, "b")
	createVM(t, c, ns, "c", func(vm *v1alpha1.VirtualMachine) { vm.Spec.Disks = []v1alpha1.VMDisk{{VolumeRef: "ghost"}} })
	reconcileVM(t, r, ns, "c")
	requireReason(t, getVM(t, c, ns, "c"), "VolumeNotFound")
}

func TestVolumeOwner(t *testing.T) {
	at := func(name string, minute int, volumes ...string) v1alpha1.VirtualMachine {
		vm := v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(fixedNow.Add(time.Duration(minute) * time.Minute))}}
		for _, volume := range volumes {
			vm.Spec.Disks = append(vm.Spec.Disks, v1alpha1.VMDisk{VolumeRef: volume})
		}
		return vm
	}
	cases := []struct {
		name string
		vms  []v1alpha1.VirtualMachine
		want string
	}{
		{"oldest wins", []v1alpha1.VirtualMachine{at("b", 1, "d"), at("a", 2, "d")}, "b"},
		{"tie by name", []v1alpha1.VirtualMachine{at("b", 1, "d"), at("a", 1, "d")}, "a"},
		{"none", []v1alpha1.VirtualMachine{at("a", 1, "other")}, ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		if got := VolumeOwner(tc.vms, "d"); got != tc.want {
			t.Errorf("%s gave %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestVMRootDiskTooSmall(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.RootDisk.SizeGi = 2 })
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	requireReason(t, getVM(t, c, ns, "web-1"), "RootDiskTooSmall")
}

func TestVMInstanceTypeAndPreferenceMustExist(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := newVMReconciler(c)
	createVM(t, c, ns, "no-type", func(vm *v1alpha1.VirtualMachine) { vm.Spec.InstanceType = "u9.none" })
	createVM(t, c, ns, "no-preference", func(vm *v1alpha1.VirtualMachine) { vm.Spec.Preference = "nope" })
	createVM(t, c, ns, "from-image", func(vm *v1alpha1.VirtualMachine) { vm.Spec.Preference = "" })
	for _, name := range []string{"no-type", "no-preference", "from-image"} {
		reconcileVM(t, r, ns, name)
	}
	requireReason(t, getVM(t, c, ns, "no-type"), "InstanceTypeNotFound")
	requireReason(t, getVM(t, c, ns, "no-preference"), "PreferenceNotFound")
	obj := requirePresent(t, c, kubeVirtVMGVK, ns, "from-image")
	if name, _, _ := unstructured.NestedString(obj.Object, "spec", "preference", "name"); name != "ubuntu" {
		t.Fatalf("preference %q", name)
	}
}

func TestVMUsesTheDefaultSubnet(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks = nil })
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	networks, _, _ := unstructured.NestedSlice(requirePresent(t, c, kubeVirtVMGVK, ns, "web-1").Object, "spec", "template", "spec", "networks")
	if len(networks) != 1 || networks[0].(map[string]any)["name"] != "default" {
		t.Fatalf("networks %+v", networks)
	}
	if got := getVM(t, c, ns, "web-1").Spec.Networks; len(got) != 0 {
		t.Fatalf("the default must not be written back: %+v", got)
	}
}

func TestVMGateOrder(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	must(t, c.Delete(context.Background(), &v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "default"}}))
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.Image.Name = "ghost-image" })
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	vm := getVM(t, c, ns, "web-1")
	requireReason(t, vm, "ImageNotFound")
	if conditionOf(vm, ConditionNetworkReady) != nil {
		t.Fatal("an earlier gate leaves NetworkReady out")
	}
}

func TestVMRunningFalseRendersHalted(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.Running = false })
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	if got, _, _ := unstructured.NestedString(requirePresent(t, c, kubeVirtVMGVK, ns, "web-1").Object, "spec", "runStrategy"); got != "Halted" {
		t.Fatalf("runStrategy %q", got)
	}
}

func TestVMReadsCloudInitSecretRef(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createObject(t, c, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "init"}, Data: map[string][]byte{"userdata": []byte("#cloud-config\nhostname: from-secret\n")}})
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.CloudInit = &v1alpha1.VMCloudInit{SecretRef: "init"} })
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	var secret corev1.Secret
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web-1-cloudinit"}, &secret))
	if !strings.Contains(string(secret.Data["userdata"]), "hostname: from-secret") {
		t.Fatalf("userdata %q", secret.Data["userdata"])
	}
}

func TestRootClaimIsNotReapplied(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	before := requirePresent(t, c, dataVolumeGVK, ns, "web-1-root")
	createPVC(t, c, ns, "web-1-root", 40, "block")
	vm := getVM(t, c, ns, "web-1")
	vm.Spec.RootDisk.SizeGi = 80
	must(t, c.Update(context.Background(), &vm))
	reconcileVM(t, r, ns, "web-1")
	after := requirePresent(t, c, dataVolumeGVK, ns, "web-1-root")
	if !reflect.DeepEqual(before.Object["spec"], after.Object["spec"]) {
		t.Fatalf("the DataVolume spec changed: %+v", after.Object["spec"])
	}
	var pvc corev1.PersistentVolumeClaim
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web-1-root"}, &pvc))
	if got := pvc.Spec.Resources.Requests.Storage(); got.String() != "80Gi" {
		t.Fatalf("PVC request %s", got)
	}
}

func TestVMDriftIsCorrected(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	key := "default." + ns + ".fabric.cloudyfolks.io/port_security"
	obj := requirePresent(t, c, kubeVirtVMGVK, ns, "web-1")
	must(t, unstructured.SetNestedField(obj.Object, "false", "spec", "template", "metadata", "annotations", key))
	must(t, c.Update(context.Background(), obj))
	reconcileVM(t, r, ns, "web-1")
	annotations, _, _ := unstructured.NestedStringMap(requirePresent(t, c, kubeVirtVMGVK, ns, "web-1").Object, "spec", "template", "metadata", "annotations")
	if annotations[key] != "true" {
		t.Fatalf("annotation %q", annotations[key])
	}
}

func TestVMRestoreHoldLeavesUnderlyingObjectsUntouched(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	versions := func() []string {
		var secret corev1.Secret
		must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web-1-cloudinit"}, &secret))
		return []string{
			requirePresent(t, c, kubeVirtVMGVK, ns, "web-1").GetResourceVersion(),
			requirePresent(t, c, dataVolumeGVK, ns, "web-1-root").GetResourceVersion(),
			secret.ResourceVersion,
		}
	}
	before := versions()
	vm := getVM(t, c, ns, "web-1")
	vm.Annotations = map[string]string{AnnotationRestoreHold: "restore-1"}
	vm.Spec.Running = false
	must(t, c.Update(context.Background(), &vm))
	reconcileVM(t, r, ns, "web-1")
	if after := versions(); !reflect.DeepEqual(before, after) {
		t.Fatalf("versions changed: %v -> %v", before, after)
	}
	requireReason(t, getVM(t, c, ns, "web-1"), "RestoreInProgress")
	vm = getVM(t, c, ns, "web-1")
	vm.Annotations = nil
	must(t, c.Update(context.Background(), &vm))
	reconcileVM(t, r, ns, "web-1")
	if got, _, _ := unstructured.NestedString(requirePresent(t, c, kubeVirtVMGVK, ns, "web-1").Object, "spec", "runStrategy"); got != "Halted" {
		t.Fatalf("runStrategy %q", got)
	}
}

func TestPVCsCarryTheOwnerLabels(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createPVC(t, c, ns, "web-1-root", 40, "block")
	createPVC(t, c, ns, "web-1-cdrom-0", 1, "block")
	createPVC(t, c, ns, "other", 1, "block")
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.CDROMs = []v1alpha1.VMCDROM{{ImageRef: "install-iso"}} })
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	want := v1alpha1.TenantLabels("VirtualMachine", "web-1", "acme", "web")
	for _, name := range []string{"web-1-root", "web-1-cdrom-0"} {
		var pvc corev1.PersistentVolumeClaim
		must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &pvc))
		if !reflect.DeepEqual(pvc.Labels, want) {
			t.Errorf("%s labels %+v", name, pvc.Labels)
		}
	}
	var other corev1.PersistentVolumeClaim
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "other"}, &other))
	if len(other.Labels) != 0 {
		t.Errorf("other labels %+v", other.Labels)
	}
}

func launcherPodFor(t *testing.T, c client.Client, ns, name, vm string, annotations, labels map[string]string) {
	t.Helper()
	labels[launcherPodLabel] = vm
	createObject(t, c, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Annotations: annotations, Labels: labels, Finalizers: []string{"test/hold"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "registry.test/c"}}},
	})
}

func TestRunningPodGetsSecurityGroupChange(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	key := "default." + ns + ".fabric.cloudyfolks.io/security_groups"
	stale := func() (map[string]string, map[string]string) {
		return map[string]string{key: "old"}, map[string]string{"sg.bedrock.cloudyfolks.io/old": "true"}
	}
	annotations, labels := stale()
	launcherPodFor(t, c, ns, "launcher-web-1", "web-1", annotations, labels)
	annotations, labels = stale()
	launcherPodFor(t, c, ns, "launcher-other", "other", annotations, labels)
	annotations, labels = stale()
	launcherPodFor(t, c, ns, "launcher-dying", "web-1", annotations, labels)
	must(t, c.Delete(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "launcher-dying"}}))
	createVM(t, c, ns, "web-1", noMutation)
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	get := func(name string) corev1.Pod {
		var pod corev1.Pod
		must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &pod))
		return pod
	}
	live := get("launcher-web-1")
	if live.Annotations[key] != FabricName(ns, "web") || live.Labels["sg.bedrock.cloudyfolks.io/web"] != "true" {
		t.Fatalf("live pod %+v %+v", live.Annotations, live.Labels)
	}
	if _, found := live.Labels["sg.bedrock.cloudyfolks.io/old"]; found {
		t.Fatalf("the stale group label stays: %+v", live.Labels)
	}
	for _, name := range []string{"launcher-other", "launcher-dying"} {
		if pod := get(name); pod.Annotations[key] != "old" {
			t.Errorf("%s was patched: %+v", name, pod.Annotations)
		}
	}
}

func TestVMIgnoresElasticIPRef(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].ElasticIPRef = "ghost" })
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	requirePresent(t, c, kubeVirtVMGVK, ns, "web-1")
	if got := getVM(t, c, ns, "web-1").Status.ElasticIP; got != "" {
		t.Fatalf("elastic IP %q", got)
	}
}

func TestVMApplyFailureIsReported(t *testing.T) {
	_, cfg := startEnv(t)
	base, err := client.NewWithWatch(cfg, client.Options{Scheme: testScheme(t)})
	must(t, err)
	ns := seedPlatform(t, base)
	applies := 0
	wrapped := interceptor.NewClient(base, interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			applies++
			if applies == 2 {
				return apierrors.NewInvalid(schema.GroupKind{Group: "kubevirt.io", Kind: "VirtualMachine"}, "web-1", nil)
			}
			return c.Apply(ctx, obj, opts...)
		},
	})
	createVM(t, base, ns, "web-1", noMutation)
	result := reconcileVM(t, newVMReconciler(wrapped), ns, "web-1")
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue %s", result.RequeueAfter)
	}
	requireReason(t, getVM(t, base, ns, "web-1"), "ApplyFailed")
}
