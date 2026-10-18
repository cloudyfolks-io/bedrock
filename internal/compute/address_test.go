package compute

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func pinned(address string) func(*v1alpha1.VirtualMachine) {
	return func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].Address = address }
}

func pinnedWithoutDisks(address string) func(*v1alpha1.VirtualMachine) {
	return func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Disks = nil
		pinned(address)(vm)
	}
}

func requireAddressGate(t *testing.T, vm v1alpha1.VirtualMachine, reason string) {
	t.Helper()
	requireReason(t, vm, reason)
	if cond := conditionOf(vm, ConditionNetworkReady); cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reason {
		t.Fatalf("NetworkReady condition %+v, want False with reason %s", cond, reason)
	}
}

func templateAnnotations(t *testing.T, c client.Client, ns, name string) map[string]string {
	t.Helper()
	obj := requirePresent(t, c, kubeVirtVMGVK, ns, name)
	annotations, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
	return annotations
}

func TestVMPinsTheRequestedAddress(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", pinned("10.100.0.50"))
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	key := "default." + ns + ".fabric.cloudyfolks.io/ip_address"
	if got := templateAnnotations(t, c, ns, "web-1")[key]; got != "10.100.0.50" {
		t.Fatalf("%s = %q", key, got)
	}
	requireReason(t, getVM(t, c, ns, "web-1"), "Starting")
	reconcileVM(t, r, ns, "web-1")
	requireReason(t, getVM(t, c, ns, "web-1"), "Starting")
}

func TestVMWithoutAddressCarriesNoAddressAnnotation(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	prefix := "default." + ns + ".fabric.cloudyfolks.io/"
	want := map[string]string{prefix + "port_security": "true", prefix + "security_groups": network.FabricName(ns, "web")}
	got := templateAnnotations(t, c, ns, "web-1")
	for key := range got {
		if strings.HasSuffix(key, "/ip_address") {
			t.Errorf("unexpected %s", key)
		}
	}
	if len(got) != len(want) || got[prefix+"port_security"] != "true" || got[prefix+"security_groups"] != want[prefix+"security_groups"] {
		t.Fatalf("annotations %+v", got)
	}
}

func TestVMRefusesAnAddressThatAnotherVMPins(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "a", pinnedWithoutDisks("10.100.0.50"))
	createVM(t, c, ns, "b", pinnedWithoutDisks("10.100.0.50"))
	r := newVMReconciler(c)
	result := reconcileVM(t, r, ns, "b")
	requireAddressGate(t, getVM(t, c, ns, "b"), ReasonAddressInUse)
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue %s", result.RequeueAfter)
	}
	requireAbsent(t, c, kubeVirtVMGVK, ns, "b")
	requireAbsent(t, c, dataVolumeGVK, ns, "b-root")
	reconcileVM(t, r, ns, "a")
	requirePresent(t, c, kubeVirtVMGVK, ns, "a")
	requireReason(t, getVM(t, c, ns, "a"), "Starting")
	must(t, c.Delete(context.Background(), &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "a"}}))
	reconcileVM(t, r, ns, "b")
	requirePresent(t, c, kubeVirtVMGVK, ns, "b")
	requireReason(t, getVM(t, c, ns, "b"), "Starting")
}

func TestVMAcceptsTheSameAddressOnAnotherSubnet(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createSubnet(t, c, ns, "backend", metav1.ConditionTrue)
	createVM(t, c, ns, "a", pinnedWithoutDisks("10.100.0.50"))
	createVM(t, c, ns, "b", func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Disks = nil
		vm.Spec.Networks = []v1alpha1.VMNetwork{{SubnetRef: "backend", Address: "10.100.0.50", SecurityGroups: []string{"web"}}}
	})
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "a")
	reconcileVM(t, r, ns, "b")
	requirePresent(t, c, kubeVirtVMGVK, ns, "b")
}

func TestVMRefusesAnAddressThatFabricAllocated(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	subnet := network.FabricName(ns, "default")
	createFabricIP(t, c, "db-1."+ns, ns, "db-1", subnet, "10.100.0.60")
	createFabricIP(t, c, "web-1."+ns, ns, "web-1", subnet, "10.100.0.61")
	createVM(t, c, ns, "web-1", pinned("10.100.0.60"))
	createVM(t, c, ns, "web-2", pinned("10.100.0.61"))
	r := newVMReconciler(c)
	result := reconcileVM(t, r, ns, "web-1")
	requireAddressGate(t, getVM(t, c, ns, "web-1"), ReasonAddressInUse)
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue %s", result.RequeueAfter)
	}
	requireAbsent(t, c, kubeVirtVMGVK, ns, "web-1")
	reconcileVM(t, r, ns, "web-2")
	requireAddressGate(t, getVM(t, c, ns, "web-2"), ReasonAddressInUse)
	deleteFabricIP(t, c, "db-1."+ns)
	reconcileVM(t, r, ns, "web-1")
	requirePresent(t, c, kubeVirtVMGVK, ns, "web-1")
}

func TestVMKeepsItsOwnFabricAddress(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createFabricIP(t, c, "web-1."+ns, ns, "web-1", network.FabricName(ns, "default"), "10.100.0.61")
	createVM(t, c, ns, "web-1", pinned("10.100.0.61"))
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	reconcileVM(t, r, ns, "web-1")
	requirePresent(t, c, kubeVirtVMGVK, ns, "web-1")
	requireReason(t, getVM(t, c, ns, "web-1"), "Starting")
}

func TestVMRefusesTheGatewayAddress(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", pinned("10.100.0.1"))
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	requireAddressGate(t, getVM(t, c, ns, "web-1"), ReasonAddressReserved)
	requireAbsent(t, c, kubeVirtVMGVK, ns, "web-1")
}

func TestVMRefusesAnExcludedAddress(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	var subnet v1alpha1.Subnet
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "default"}, &subnet))
	subnet.Spec.ExcludeIPs = []string{"10.100.0.20..10.100.0.30"}
	must(t, c.Update(context.Background(), &subnet))
	createVM(t, c, ns, "web-1", pinned("10.100.0.25"))
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	requireAddressGate(t, getVM(t, c, ns, "web-1"), ReasonAddressReserved)
	requireAbsent(t, c, kubeVirtVMGVK, ns, "web-1")
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(&subnet), &subnet))
	subnet.Spec.ExcludeIPs = nil
	must(t, c.Update(context.Background(), &subnet))
	reconcileVM(t, r, ns, "web-1")
	requirePresent(t, c, kubeVirtVMGVK, ns, "web-1")
}

func TestVMRefusesAnAddressOutsideTheSubnet(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", pinned("10.200.0.5"))
	reconcileVM(t, newVMReconciler(c), ns, "web-1")
	requireAddressGate(t, getVM(t, c, ns, "web-1"), ReasonAddressOutsideSubnet)
	requireAbsent(t, c, kubeVirtVMGVK, ns, "web-1")
}

func TestPinnedBy(t *testing.T) {
	at := func(name string, created int64, subnet, address string) v1alpha1.VirtualMachine {
		return v1alpha1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(time.Unix(created, 0))},
			Spec:       v1alpha1.VirtualMachineSpec{Networks: []v1alpha1.VMNetwork{{SubnetRef: subnet, Address: address}}},
		}
	}
	vms := []v1alpha1.VirtualMachine{
		at("old", 100, "default", "10.100.0.50"),
		at("young", 300, "default", "10.100.0.51"),
		at("tie-a", 200, "default", "10.100.0.52"),
		at("tie-b", 200, "default", "10.100.0.52"),
		at("elsewhere", 100, "backend", "10.100.0.53"),
		at("auto", 100, "default", ""),
	}
	cases := map[string]struct {
		vm      v1alpha1.VirtualMachine
		subnet  string
		address string
		holder  string
	}{
		"an older VM holds it":           {at("me", 200, "default", "10.100.0.50"), "default", "10.100.0.50", "old"},
		"a younger VM does not hold it":  {at("me", 200, "default", "10.100.0.51"), "default", "10.100.0.51", ""},
		"the same VM does not hold it":   {at("old", 100, "default", "10.100.0.50"), "default", "10.100.0.50", ""},
		"the name breaks a tie":          {at("tie-b", 200, "default", "10.100.0.52"), "default", "10.100.0.52", "tie-a"},
		"the first name wins a tie":      {at("tie-a", 200, "default", "10.100.0.52"), "default", "10.100.0.52", ""},
		"another subnet is no conflict":  {at("me", 200, "default", "10.100.0.53"), "default", "10.100.0.53", ""},
		"a free address":                 {at("me", 200, "default", "10.100.0.99"), "default", "10.100.0.99", ""},
		"the older VM of another subnet": {at("me", 200, "backend", "10.100.0.53"), "backend", "10.100.0.53", "elsewhere"},
	}
	for label, tc := range cases {
		holder, held := pinnedBy(vms, tc.vm, tc.subnet, tc.address)
		if holder != tc.holder || held != (tc.holder != "") {
			t.Errorf("%s: got %q %v want %q", label, holder, held, tc.holder)
		}
	}
}

func requestAnnotations(ns, ip string) map[string]string {
	return map[string]string{"default." + ns + ".fabric.cloudyfolks.io/ip_address": ip}
}

func allocatedAnnotations(ns, ip string) map[string]string {
	annotations := requestAnnotations(ns, ip)
	annotations["default."+ns+".fabric.cloudyfolks.io/allocated"] = "true"
	return annotations
}

func TestVMThatHoldsItsAddressIsNotInConflict(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", pinned("10.100.0.50"))
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	launcherPodFor(t, c, ns, "launcher-web-1", "web-1", allocatedAnnotations(ns, "10.100.0.50"), map[string]string{})
	reconcileVM(t, r, ns, "web-1")
	if got := getVM(t, c, ns, "web-1").Status.Addresses; len(got) != 1 || got[0].IP != "10.100.0.50" {
		t.Fatalf("addresses %+v", got)
	}
	createFabricIP(t, c, "launcher."+ns, ns, "virt-launcher-web-1-x7k2p", network.FabricName(ns, "default"), "10.100.0.50")
	reconcileVM(t, r, ns, "web-1")
	if reason := readyOf(getVM(t, c, ns, "web-1")).Reason; reason == ReasonAddressInUse {
		t.Fatalf("a VM that holds the address must not conflict with its own Fabric IP: %s", reason)
	}
}

func TestVMWhoseRequestFabricRefusedReportsAddressInUse(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", pinned("10.100.0.50"))
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	requireReason(t, getVM(t, c, ns, "web-1"), "Starting")
	launcherPodFor(t, c, ns, "launcher-web-1", "web-1", requestAnnotations(ns, "10.100.0.50"), map[string]string{})
	createFabricIP(t, c, "db-1."+ns, ns, "db-1", network.FabricName(ns, "default"), "10.100.0.50")
	for round := 0; round < 3; round++ {
		result := reconcileVM(t, r, ns, "web-1")
		vm := getVM(t, c, ns, "web-1")
		requireAddressGate(t, vm, ReasonAddressInUse)
		if result.RequeueAfter != 30*time.Second || len(vm.Status.Addresses) != 0 {
			t.Fatalf("round %d: requeue %s addresses %+v", round, result.RequeueAfter, vm.Status.Addresses)
		}
	}
}

func TestPinnedVMRequeuesUntilItsAddressIsAllocated(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", pinned("10.100.0.50"))
	createVM(t, c, ns, "halted", func(vm *v1alpha1.VirtualMachine) {
		pinnedWithoutDisks("10.100.0.51")(vm)
		vm.Spec.Running = false
	})
	r := newVMReconciler(c)
	if result := reconcileVM(t, r, ns, "web-1"); result.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue %s", result.RequeueAfter)
	}
	if result := reconcileVM(t, r, ns, "halted"); result.RequeueAfter != 0 {
		t.Fatalf("a stopped VM waits for nothing: requeue %s", result.RequeueAfter)
	}
	launcherPodFor(t, c, ns, "launcher-web-1", "web-1", requestAnnotations(ns, "10.100.0.50"), map[string]string{})
	if result := reconcileVM(t, r, ns, "web-1"); result.RequeueAfter != 30*time.Second {
		t.Fatalf("a request that Fabric has not allocated: requeue %s", result.RequeueAfter)
	}
	var pod corev1.Pod
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "launcher-web-1"}, &pod))
	pod.Annotations["default."+ns+".fabric.cloudyfolks.io/allocated"] = "true"
	must(t, c.Update(context.Background(), &pod))
	if result := reconcileVM(t, r, ns, "web-1"); result.RequeueAfter != 0 {
		t.Fatalf("an allocated address: requeue %s", result.RequeueAfter)
	}
	if got := getVM(t, c, ns, "web-1").Status.Addresses; len(got) != 1 || got[0].IP != "10.100.0.50" {
		t.Fatalf("addresses %+v", got)
	}
}

func TestRunningPodKeepsItsAddressWhenTheNICIsAttachedAgain(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createSubnet(t, c, ns, "backend", metav1.ConditionTrue)
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Disks = nil
		vm.Spec.Networks = []v1alpha1.VMNetwork{{SubnetRef: "default", Address: "10.100.0.50", SecurityGroups: []string{"web"}}, {SubnetRef: "backend", SecurityGroups: []string{"web"}}}
	})
	r := newVMReconciler(c)
	launcherPodFor(t, c, ns, "launcher-web-1", "web-1", allocatedAnnotations(ns, "10.100.0.50"), map[string]string{})
	reconcileVM(t, r, ns, "web-1")
	reconcileVM(t, r, ns, "web-1")
	setNetworks := func(networks []v1alpha1.VMNetwork) {
		vm := getVM(t, c, ns, "web-1")
		vm.Spec.Networks = networks
		must(t, c.Update(context.Background(), &vm))
		reconcileVM(t, r, ns, "web-1")
	}
	setNetworks([]v1alpha1.VMNetwork{{SubnetRef: "backend", SecurityGroups: []string{"web"}}})
	setNetworks([]v1alpha1.VMNetwork{{SubnetRef: "backend", SecurityGroups: []string{"web"}}, {SubnetRef: "default", Address: "10.100.0.77", SecurityGroups: []string{"web"}}})
	var pod corev1.Pod
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "launcher-web-1"}, &pod))
	if got := pod.Annotations["default."+ns+".fabric.cloudyfolks.io/ip_address"]; got != "10.100.0.50" {
		t.Fatalf("the running pod ip_address is %q", got)
	}
	if got := getVM(t, c, ns, "web-1").Status.Addresses; len(got) != 1 || got[0].IP != "10.100.0.50" {
		t.Fatalf("addresses %+v", got)
	}
	if pod.Annotations["default."+ns+".fabric.cloudyfolks.io/security_groups"] != network.FabricName(ns, "web") {
		t.Fatalf("the other annotations still follow the VM: %+v", pod.Annotations)
	}
}
