package network

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func takenOf(addresses ...string) map[netip.Addr]bool {
	taken := map[netip.Addr]bool{}
	for _, address := range addresses {
		taken[netip.MustParseAddr(address)] = true
	}
	return taken
}

func TestLowestFreeAddress(t *testing.T) {
	cases := map[string]struct {
		subnet v1alpha1.Subnet
		taken  map[netip.Addr]bool
		want   string
	}{
		"empty subnet skips the gateway": {nicSubnet(""), takenOf(), "10.100.0.2"},
		"first taken":                    {nicSubnet(""), takenOf("10.100.0.2"), "10.100.0.3"},
		"a gap is filled":                {nicSubnet(""), takenOf("10.100.0.2", "10.100.0.4"), "10.100.0.3"},
		"named gateway frees the first":  {nicSubnet("10.100.0.254"), takenOf(), "10.100.0.1"},
		"excluded range is skipped":      {nicSubnet("", "10.100.0.2..10.100.0.9"), takenOf(), "10.100.0.10"},
		"excluded and taken mix":         {nicSubnet("", "10.100.0.2..10.100.0.4"), takenOf("10.100.0.5"), "10.100.0.6"},
		"outside addresses are ignored":  {nicSubnet(""), takenOf("10.200.0.2"), "10.100.0.2"},
	}
	for name, tc := range cases {
		got, err := LowestFreeAddress(tc.subnet, tc.taken)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q %v, want %q", name, got, err, tc.want)
		}
	}
}

func TestLowestFreeAddressOfAFullSubnet(t *testing.T) {
	subnet := v1alpha1.Subnet{Spec: v1alpha1.SubnetSpec{CIDR: "10.100.0.0/30"}}
	got, err := LowestFreeAddress(subnet, takenOf())
	if err != nil || got != "10.100.0.2" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := LowestFreeAddress(subnet, takenOf("10.100.0.2")); !errors.Is(err, ErrNoFreeAddress) {
		t.Fatalf("err %v", err)
	}
	if _, err := LowestFreeAddress(v1alpha1.Subnet{Spec: v1alpha1.SubnetSpec{CIDR: "nonsense"}}, takenOf()); err == nil {
		t.Fatal("a bad CIDR was accepted")
	}
}

func TestTakenAddressesReadFabricIPsMachinesContainersAndReservations(t *testing.T) {
	c, _ := testEnv(t)
	ctx := context.Background()
	namespace := newNamespace(t, unique(t, "alloc"), "p")
	subnet := v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: namespace}, Spec: v1alpha1.SubnetSpec{NetworkRef: "n", CIDR: "10.100.0.0/24"}}

	ip := fabricIP(unique(t, "ip"), namespace, "pod-1", "10.100.0.2")
	ip.SetLabels(map[string]string{labelFabricSubnet: FabricName(namespace, "default")})
	otherSubnetIP := fabricIP(unique(t, "ip"), namespace, "pod-2", "10.100.0.3")
	otherSubnetIP.SetLabels(map[string]string{labelFabricSubnet: FabricName(namespace, "other")})
	for _, obj := range []client.Object{ip, otherSubnetIP} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	machine := &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "vm", Namespace: namespace}, Spec: v1alpha1.VirtualMachineSpec{
		InstanceType: "small", RootDisk: v1alpha1.VMRootDisk{SizeGi: 10},
		Networks: []v1alpha1.VMNetwork{{SubnetRef: "default", Address: "10.100.0.4"}, {SubnetRef: "other", Address: "10.100.0.5"}},
	}}
	pinned := &v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Name: "pinned", Namespace: namespace}, Spec: v1alpha1.ContainerSpec{Image: "x", Network: v1alpha1.ContainerNetwork{Address: "10.100.0.6"}}}
	elsewhere := &v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: namespace}, Spec: v1alpha1.ContainerSpec{Image: "x", Network: v1alpha1.ContainerNetwork{SubnetRef: "other", Address: "10.100.0.7"}}}
	for _, obj := range []client.Object{machine, pinned, elsewhere} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}

	taken, err := TakenAddresses(ctx, c, subnet, []string{"10.100.0.8", "bad"})
	if err != nil {
		t.Fatal(err)
	}
	want := takenOf("10.100.0.2", "10.100.0.4", "10.100.0.6", "10.100.0.8")
	if len(taken) != len(want) {
		t.Fatalf("taken %v", taken)
	}
	for addr := range want {
		if !taken[addr] {
			t.Fatalf("taken %v lacks %s", taken, addr)
		}
	}
	got, err := AllocateAddress(ctx, c, subnet, []string{"10.100.0.8"})
	if err != nil || got != "10.100.0.3" {
		t.Fatalf("allocated %q %v", got, err)
	}
}
