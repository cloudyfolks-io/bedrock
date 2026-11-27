package clusters

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func addr(text string) netip.Addr {
	return netip.MustParseAddr(text)
}

func addrs(texts ...string) []netip.Addr {
	out := make([]netip.Addr, len(texts))
	for i, text := range texts {
		out[i] = addr(text)
	}
	return out
}

func TestNextFreeAddressStartsAtOffsetTen(t *testing.T) {
	got, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/24"), addr("10.100.0.1"), nil, nil)
	if err != nil || got != addr("10.100.0.10") {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestNextFreeAddressIgnoresTheHostBitsOfTheCIDR(t *testing.T) {
	got, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.77/24"), addr("10.100.0.1"), nil, nil)
	if err != nil || got != addr("10.100.0.10") {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestNextFreeAddressSkipsTheGateway(t *testing.T) {
	got, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/24"), addr("10.100.0.10"), nil, nil)
	if err != nil || got != addr("10.100.0.11") {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestNextFreeAddressSkipsExcludedAddressesAndRanges(t *testing.T) {
	got, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/24"), addr("10.100.0.1"), []string{"10.100.0.10", "10.100.0.11..10.100.0.14"}, nil)
	if err != nil || got != addr("10.100.0.15") {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestNextFreeAddressRefusesABrokenExcludeEntry(t *testing.T) {
	_, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/24"), addr("10.100.0.1"), []string{"10.100.0.20..10.100.0.15"}, nil)
	var failure *AddressError
	if !errors.As(err, &failure) || failure.Reason != "ExcludeIPsInvalid" {
		t.Fatalf("error %v", err)
	}
}

func TestNextFreeAddressSkipsTakenAddresses(t *testing.T) {
	got, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/24"), addr("10.100.0.1"), nil, addrs("10.100.0.10", "10.100.0.12", "10.100.0.11"))
	if err != nil || got != addr("10.100.0.13") {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestNextFreeAddressIgnoresTakenAddressesOfOtherSubnets(t *testing.T) {
	got, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/24"), addr("10.100.0.1"), nil, addrs("10.200.0.10"))
	if err != nil || got != addr("10.100.0.10") {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestNextFreeAddressOfAFullSubnetIsExhausted(t *testing.T) {
	var taken []netip.Addr
	for last := 10; last <= 254; last++ {
		taken = append(taken, netip.AddrFrom4([4]byte{10, 100, 0, byte(last)}))
	}
	_, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/24"), addr("10.100.0.1"), nil, taken)
	var failure *AddressError
	if !errors.As(err, &failure) || failure.Reason != ReasonAddressExhausted {
		t.Fatalf("error %v", err)
	}
}

func TestNextFreeAddressNeverReturnsTheBroadcastAddress(t *testing.T) {
	taken := addrs("10.100.0.10", "10.100.0.11", "10.100.0.12", "10.100.0.13")
	got, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/28"), addr("10.100.0.1"), nil, taken)
	if err != nil || got != addr("10.100.0.14") {
		t.Fatalf("got %v %v", got, err)
	}
	_, err = NextFreeAddress(netip.MustParsePrefix("10.100.0.0/28"), addr("10.100.0.1"), nil, append(taken, addr("10.100.0.14")))
	var failure *AddressError
	if !errors.As(err, &failure) || failure.Reason != ReasonAddressExhausted {
		t.Fatalf("a /28 has five candidates, error %v", err)
	}
}

func TestNextFreeAddressOfASubnetTooSmallForTheOffset(t *testing.T) {
	_, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/29"), addr("10.100.0.1"), nil, nil)
	var failure *AddressError
	if !errors.As(err, &failure) || failure.Reason != ReasonAddressExhausted {
		t.Fatalf("error %v", err)
	}
}

func TestNextFreeAddressAtTheTopOfTheAddressSpace(t *testing.T) {
	_, err := NextFreeAddress(netip.MustParsePrefix("255.255.255.240/28"), addr("255.255.255.241"), nil, addrs("255.255.255.250", "255.255.255.251", "255.255.255.252", "255.255.255.253", "255.255.255.254"))
	var failure *AddressError
	if !errors.As(err, &failure) || failure.Reason != ReasonAddressExhausted {
		t.Fatalf("error %v", err)
	}
}

func fabricIP(name, subnet, address string) *unstructured.Unstructured {
	ip := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"v4IpAddress": address}}}
	ip.SetGroupVersionKind(fabric.IPGVK)
	ip.SetName(name)
	ip.SetLabels(map[string]string{fabricSubnetLabel: subnet})
	return ip
}

func podFabricIP(name, subnet, address, namespace, pod string) *unstructured.Unstructured {
	ip := fabricIP(name, subnet, address)
	_ = unstructured.SetNestedField(ip.Object, namespace, "spec", "namespace")
	_ = unstructured.SetNestedField(ip.Object, pod, "spec", "podName")
	return ip
}

func clusterWithAddress(name, address string) *v1alpha1.KubernetesCluster {
	return &v1alpha1.KubernetesCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: "t-a-web", Name: name},
		Status:     v1alpha1.KubernetesClusterStatus{ControlPlane: v1alpha1.KubernetesControlPlaneStatus{Address: address}},
	}
}

func pinnedCluster(name, address string) *v1alpha1.KubernetesCluster {
	cluster := clusterWithAddress(name, "")
	cluster.Spec.ControlPlane.Address = address
	return cluster
}

func addressReader(t *testing.T, objects ...client.Object) AddressReader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	scheme.AddKnownTypeWithName(fabric.IPGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(fabric.IPGVK.GroupVersion().WithKind("IPList"), &unstructured.UnstructuredList{})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return AddressReader{Clusters: c, IPs: c}
}

func TestTakenReadsFabricIPsOfTheSubnetAndOtherClusters(t *testing.T) {
	reader := addressReader(t,
		fabricIP("ip-a", network.FabricName("t-a-web", "default"), "10.100.0.10"),
		fabricIP("ip-b", network.FabricName("t-a-web", "default"), "10.100.0.11"),
		fabricIP("ip-other-subnet", network.FabricName("t-a-web", "other"), "10.100.0.12"),
		clusterWithAddress("other", "10.100.0.13"),
		clusterWithAddress("own", "10.100.0.14"),
		clusterWithAddress("unassigned", ""),
		podFabricIP("ip-own-pod", network.FabricName("t-a-web", "default"), "10.100.0.15", "t-a-web", "kmc-own-0"),
		podFabricIP("ip-other-pod", network.FabricName("t-a-web", "default"), "10.100.0.17", "t-a-web", "kmc-other-0"),
		pinnedCluster("pinned", "10.100.0.16"),
	)
	taken, err := reader.Taken(context.Background(), *clusterWithAddress("own", ""), "default")
	if err != nil {
		t.Fatal(err)
	}
	want := map[netip.Addr]bool{addr("10.100.0.10"): true, addr("10.100.0.11"): true, addr("10.100.0.13"): true, addr("10.100.0.17"): true}
	if len(taken) != len(want) {
		t.Fatalf("taken %v", taken)
	}
	for _, a := range taken {
		if !want[a] {
			t.Errorf("unexpected address %v in %v", a, taken)
		}
	}
}

func TestTakenFeedsNextFreeAddress(t *testing.T) {
	reader := addressReader(t, fabricIP("ip-a", network.FabricName("t-a-web", "default"), "10.100.0.10"), clusterWithAddress("other", "10.100.0.11"))
	taken, err := reader.Taken(context.Background(), *clusterWithAddress("own", ""), "default")
	if err != nil {
		t.Fatal(err)
	}
	got, err := NextFreeAddress(netip.MustParsePrefix("10.100.0.0/24"), addr("10.100.0.1"), nil, taken)
	if err != nil || got != addr("10.100.0.12") {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestSubnetGatewayUsesTheSpecThenTheDefault(t *testing.T) {
	explicit := v1alpha1.Subnet{Spec: v1alpha1.SubnetSpec{CIDR: "10.100.0.0/24", Gateway: "10.100.0.254"}}
	if got, err := SubnetGateway(explicit); err != nil || got != addr("10.100.0.254") {
		t.Fatalf("got %v %v", got, err)
	}
	implicit := v1alpha1.Subnet{Spec: v1alpha1.SubnetSpec{CIDR: "10.100.0.0/24"}}
	if got, err := SubnetGateway(implicit); err != nil || got != addr("10.100.0.1") {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestPinnedAddressesAreTheSpecAddressesOfOtherClusters(t *testing.T) {
	clusters := []v1alpha1.KubernetesCluster{*pinnedCluster("a", "10.100.0.20"), *pinnedCluster("b", "10.100.0.21"), *pinnedCluster("c", ""), *clusterWithAddress("d", "10.100.0.22")}
	got := pinnedAddresses(clusters, "a")
	if len(got) != 1 || got[0] != addr("10.100.0.21") {
		t.Fatalf("got %v", got)
	}
}
