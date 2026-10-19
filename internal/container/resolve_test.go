package container

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func labelsOf(namespace, name string) map[string]string {
	return v1alpha1.TenantLabels(Kind, name, "acme", "web")
}

func resolveContainer(t *testing.T, c client.Client, container v1alpha1.Container) (RenderInput, *Gate) {
	t.Helper()
	in, gate, err := newResolver(c).Resolve(context.Background(), container, labelsOf(container.Namespace, container.Name))
	must(t, err)
	return in, gate
}

func requireGate(t *testing.T, gate *Gate, reason string) {
	t.Helper()
	if gate == nil || gate.Reason != reason {
		t.Fatalf("gate %+v, want reason %s", gate, reason)
	}
}

func TestResolveFillsTheRenderInput(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) {
		s.Network.SecurityGroups = []string{"web"}
		s.Network.Address = "10.100.0.50"
	})
	in, gate := resolveContainer(t, c, container)
	if gate != nil {
		t.Fatalf("gate %+v", gate)
	}
	want := network.NIC{Namespace: ns, Subnet: "default", Address: "10.100.0.50", SecurityGroups: []string{"web"}}
	if !reflect.DeepEqual(in.NIC, want) {
		t.Fatalf("nic %+v", in.NIC)
	}
	if in.RuntimeClass != "kata" || in.Container.Name != "web" || in.Labels[v1alpha1.LabelKind] != "Container" {
		t.Fatalf("input %+v", in)
	}
}

func TestResolveRefusesAnotherNamespaceWithoutLabelsInTheController(t *testing.T) {
	if _, ok := OwnerLabels("web", corev1Namespace(nil)); ok {
		t.Fatal("a namespace without labels is not a project")
	}
	labels, ok := OwnerLabels("web", corev1Namespace(map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "web"}))
	if !ok || !reflect.DeepEqual(labels, v1alpha1.TenantLabels("Container", "web", "acme", "web")) {
		t.Fatalf("labels %v %v", labels, ok)
	}
}

func TestResolveSubnetNotFound(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Network.SubnetRef = "missing" })
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonSubnetNotFound)
}

func TestResolveSubnetNotReady(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createSubnet(t, c, ns, "slow", metav1.ConditionFalse)
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Network.SubnetRef = "slow" })
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonSubnetNotReady)
}

func TestResolveSubnetNetworkWithoutVpc(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	other := &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "novpc" + uniqueNamespaceSuffix()}, Spec: v1alpha1.NetworkSpec{TenantRef: "novpc", CIDRs: []string{"10.200.0.0/16"}}}
	createObject(t, c, other)
	subnet := &v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "orphan"}, Spec: v1alpha1.SubnetSpec{NetworkRef: other.Name, CIDR: "10.200.0.0/24"}}
	createObject(t, c, subnet)
	updateStatus(t, c, subnet, func(s *v1alpha1.Subnet) { s.Status.Conditions = readyConditions(metav1.ConditionTrue) })
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Network.SubnetRef = "orphan" })
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonSubnetNotReady)
	if !strings.Contains(gate.Message, "network") {
		t.Fatalf("message %q", gate.Message)
	}
}

func TestResolveDNSServersOfTheSubnetThenOfTheSetting(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	container := createContainer(t, c, ns, "web", noMutation)
	in, gate := resolveContainer(t, c, container)
	if gate != nil || len(in.DNSServers) != 0 {
		t.Fatalf("gate %+v dns %v", gate, in.DNSServers)
	}
	custom := &v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "custom"}, Spec: v1alpha1.SubnetSpec{NetworkRef: "acme", CIDR: "10.100.1.0/24", DNSServers: []string{"10.100.1.2"}}}
	createObject(t, c, custom)
	updateStatus(t, c, custom, func(s *v1alpha1.Subnet) { s.Status.Conditions = readyConditions(metav1.ConditionTrue) })
	withDNS := createContainer(t, c, ns, "dns", func(s *v1alpha1.ContainerSpec) { s.Network.SubnetRef = "custom" })
	in, gate = resolveContainer(t, c, withDNS)
	if gate != nil || !reflect.DeepEqual(in.DNSServers, []string{"10.100.1.2"}) {
		t.Fatalf("gate %+v dns %v", gate, in.DNSServers)
	}
}

func TestResolveAddressOutsideSubnet(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Network.Address = "10.101.0.5" })
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonAddressOutsideSubnet)
}

func TestResolveAddressReserved(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Network.Address = "10.100.0.0" })
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonAddressReserved)
}

func TestResolveAddressPinnedByAnOlderContainer(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	pin := func(s *v1alpha1.ContainerSpec) { s.Network.Address = "10.100.0.60" }
	first := createContainer(t, c, ns, "first", pin)
	time.Sleep(1100 * time.Millisecond)
	second := createContainer(t, c, ns, "second", pin)
	if _, gate := resolveContainer(t, c, first); gate != nil {
		t.Fatalf("the older container keeps the address: %+v", gate)
	}
	_, gate := resolveContainer(t, c, second)
	requireGate(t, gate, ReasonAddressInUse)
	if !strings.Contains(gate.Message, "first") {
		t.Fatalf("message %q", gate.Message)
	}
}

func TestResolveAddressAllocatedToAnotherPod(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createFabricIP(t, c, "other-pod."+ns, ns, "other-pod", network.FabricName(ns, "default"), "10.100.0.70")
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Network.Address = "10.100.0.70" })
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonAddressInUse)
}

func TestResolveAddressHeldByAPodOfTheSameContainerIsFree(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Network.Address = "10.100.0.80" })
	createPod(t, c, ns, "web-6d8f-abcde", "web")
	createFabricIP(t, c, "web-6d8f-abcde."+ns, ns, "web-6d8f-abcde", network.FabricName(ns, "default"), "10.100.0.80")
	if _, gate := resolveContainer(t, c, container); gate != nil {
		t.Fatalf("a pod of the container may hold the address: %+v", gate)
	}
}

func TestResolveSecurityGroupNotReady(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createSecurityGroup(t, c, ns, "slow", metav1.ConditionFalse)
	for _, name := range []string{"slow", "missing"} {
		container := createContainer(t, c, ns, "web-"+name, func(s *v1alpha1.ContainerSpec) { s.Network.SecurityGroups = []string{name} })
		_, gate := resolveContainer(t, c, container)
		requireGate(t, gate, ReasonSecurityGroupNotReady)
	}
}

func TestResolveWaitsForTheDefaultGroup(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	ensureNetwork(t, c)
	createSubnet(t, c, ns, "default", metav1.ConditionTrue)
	ensureRuntimeClass(t, c, "kata")
	container := createContainer(t, c, ns, "web", noMutation)
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonWaitingForDefaultGroup)
	ensureDefaultGroup(t, c, ns)
	if _, gate := resolveContainer(t, c, container); gate != nil {
		t.Fatalf("gate %+v", gate)
	}
}

func TestResolveVolumeNotFound(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) {
		s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "missing", MountPath: "/data"}}
	})
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonVolumeNotFound)
}

func TestResolveVolumeClassUnsupported(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVolume(t, c, ns, "block", "block")
	createVolume(t, c, ns, "classless", "")
	for _, name := range []string{"block", "classless"} {
		container := createContainer(t, c, ns, "web-"+name, func(s *v1alpha1.ContainerSpec) {
			s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: name, MountPath: "/data"}}
		})
		_, gate := resolveContainer(t, c, container)
		requireGate(t, gate, ReasonVolumeClassUnsupported)
	}
}

func TestResolveVolumeInUse(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVolume(t, c, ns, "shared", "filesystem")
	mount := func(s *v1alpha1.ContainerSpec) {
		s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "shared", MountPath: "/data"}}
	}
	first := createContainer(t, c, ns, "first", mount)
	time.Sleep(1100 * time.Millisecond)
	second := createContainer(t, c, ns, "second", mount)
	in, gate := resolveContainer(t, c, first)
	if gate != nil || len(in.Container.Spec.Volumes) != 1 {
		t.Fatalf("the first container holds the volume: %+v", gate)
	}
	_, gate = resolveContainer(t, c, second)
	requireGate(t, gate, ReasonVolumeInUse)
	if !strings.Contains(gate.Message, "Container/first") {
		t.Fatalf("message %q", gate.Message)
	}
}

func TestResolveVolumeUsedByAVirtualMachine(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVolume(t, c, ns, "disk", "filesystem")
	vm := &v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "vm1"},
		Spec: v1alpha1.VirtualMachineSpec{
			InstanceType: "u1.nano", RootDisk: v1alpha1.VMRootDisk{SizeGi: 1},
			Disks: []v1alpha1.VMDisk{{VolumeRef: "disk"}}, Running: true,
		},
	}
	createObject(t, c, vm)
	time.Sleep(1100 * time.Millisecond)
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) {
		s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "disk", MountPath: "/data"}}
	})
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonVolumeInUse)
	if !strings.Contains(gate.Message, "VirtualMachine/vm1") {
		t.Fatalf("message %q", gate.Message)
	}
}

func TestResolveRuntimeUnavailable(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	setRuntimeSetting(t, c, "no-such-class")
	container := createContainer(t, c, ns, "web", noMutation)
	_, gate := resolveContainer(t, c, container)
	requireGate(t, gate, ReasonRuntimeUnavailable)
	if !strings.Contains(gate.Message, "no-such-class") || !strings.Contains(gate.Message, RuntimeSetting) {
		t.Fatalf("message %q", gate.Message)
	}
}

func TestResolveRuntimeFollowsTheSetting(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{{"", "kata"}, {"kata", "kata"}, {RuntimeNone, ""}, {"gvisor", "gvisor"}}
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	ensureRuntimeClass(t, c, "gvisor")
	container := createContainer(t, c, ns, "web", noMutation)
	for _, tc := range cases {
		setRuntimeSetting(t, c, tc.value)
		in, gate := resolveContainer(t, c, container)
		if gate != nil || in.RuntimeClass != tc.want {
			t.Fatalf("value %q: gate %+v runtime %q, want %q", tc.value, gate, in.RuntimeClass, tc.want)
		}
	}
}

func TestResolveRuntimeWithoutASettingObjectUsesKata(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	container := createContainer(t, c, ns, "web", noMutation)
	in, gate := resolveContainer(t, c, container)
	if gate != nil || in.RuntimeClass != "kata" {
		t.Fatalf("gate %+v runtime %q", gate, in.RuntimeClass)
	}
}

func TestResolvePullSecretNotFound(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createPullSecret(t, c, ns, "registry")
	good := createContainer(t, c, ns, "good", func(s *v1alpha1.ContainerSpec) { s.ImagePullSecrets = []string{"registry"} })
	if _, gate := resolveContainer(t, c, good); gate != nil {
		t.Fatalf("gate %+v", gate)
	}
	bad := createContainer(t, c, ns, "bad", func(s *v1alpha1.ContainerSpec) { s.ImagePullSecrets = []string{"registry", "missing"} })
	_, gate := resolveContainer(t, c, bad)
	requireGate(t, gate, ReasonPullSecretNotFound)
}

func TestVolumeHolderPrefersTheOldestConsumer(t *testing.T) {
	at := func(seconds int) metav1.Time {
		return metav1.NewTime(time.Date(2026, 10, 19, 10, 0, seconds, 0, time.UTC))
	}
	vm := v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "n", Name: "vm", CreationTimestamp: at(5)}, Spec: v1alpha1.VirtualMachineSpec{Disks: []v1alpha1.VMDisk{{VolumeRef: "data"}}}}
	older := v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Namespace: "n", Name: "older", CreationTimestamp: at(1)}, Spec: v1alpha1.ContainerSpec{Volumes: []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/d"}}}}
	newer := v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Namespace: "n", Name: "newer", CreationTimestamp: at(9)}, Spec: v1alpha1.ContainerSpec{Volumes: []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/d"}}}}
	other := v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Namespace: "n", Name: "other", CreationTimestamp: at(0)}, Spec: v1alpha1.ContainerSpec{Volumes: []v1alpha1.ContainerVolume{{VolumeRef: "else", MountPath: "/d"}}}}
	holder, held := VolumeHolder("data", []v1alpha1.VirtualMachine{vm}, []v1alpha1.Container{newer, older, other})
	if !held || holder != (Holder{Kind: "Container", Name: "older"}) || holder.String() != "Container/older" {
		t.Fatalf("holder %+v %v", holder, held)
	}
	holder, held = VolumeHolder("data", []v1alpha1.VirtualMachine{vm}, []v1alpha1.Container{newer})
	if !held || holder != (Holder{Kind: "VirtualMachine", Name: "vm"}) {
		t.Fatalf("holder %+v %v", holder, held)
	}
	if _, held := VolumeHolder("free", []v1alpha1.VirtualMachine{vm}, []v1alpha1.Container{newer, older}); held {
		t.Fatal("a free volume has no holder")
	}
}
