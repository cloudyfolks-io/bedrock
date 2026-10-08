package network

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func request(namespace, name string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

func TestOwnerRequestsReadsOwnerAnnotations(t *testing.T) {
	obj := fabric.OvnEip(fabric.Meta{Name: "e", Annotations: map[string]string{AnnotationOwnerNamespace: "ns", AnnotationOwnerName: "ip1", AnnotationOwnerUID: "u"}}, "s", "")
	got := OwnerRequests(obj)
	if len(got) != 1 || got[0] != request("ns", "ip1") {
		t.Fatalf("requests %v", got)
	}
}

func TestOwnerRequestsIgnoresObjectsWithoutOwner(t *testing.T) {
	if got := OwnerRequests(fabric.OvnEip(fabric.Meta{Name: "e"}, "s", "")); len(got) != 0 {
		t.Fatalf("requests %v", got)
	}
}

func subnetOfNetwork(namespace, name, network, zone string) v1alpha1.Subnet {
	return v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Spec: v1alpha1.SubnetSpec{NetworkRef: network, DNSZoneRef: zone}}
}

func TestNetworkOfSubnetUsesNetworkRef(t *testing.T) {
	got := NetworkOfSubnet(subnetOfNetwork("ns", "s", "net1", ""))
	if len(got) != 1 || got[0] != request("", "net1") {
		t.Fatalf("requests %v", got)
	}
}

func TestSubnetsOfNetworkSelectsByNetworkRef(t *testing.T) {
	subnets := []v1alpha1.Subnet{subnetOfNetwork("a", "one", "net1", ""), subnetOfNetwork("b", "two", "net2", ""), subnetOfNetwork("c", "three", "net1", "")}
	got := SubnetsOfNetwork(subnets, "net1")
	if len(got) != 2 || got[0] != request("a", "one") || got[1] != request("c", "three") {
		t.Fatalf("requests %v", got)
	}
}

func TestZonesOfSubnetUsesNamespaceAndRef(t *testing.T) {
	got := ZonesOfSubnet(subnetOfNetwork("ns", "s", "net1", "internal"))
	if len(got) != 1 || got[0] != request("ns", "internal") {
		t.Fatalf("requests %v", got)
	}
	if len(ZonesOfSubnet(subnetOfNetwork("ns", "s", "net1", ""))) != 0 {
		t.Fatal("no reference, no request")
	}
}

func TestSecurityGroupsOfNamespaceListsAll(t *testing.T) {
	groups := []v1alpha1.SecurityGroup{{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "a"}}, {ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "b"}}}
	if got := SecurityGroupsOfNamespace(groups); len(got) != 2 || got[1] != request("ns", "b") {
		t.Fatalf("requests %v", got)
	}
}

func TestReferrersOfGroup(t *testing.T) {
	groups := []v1alpha1.SecurityGroup{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "db"}, Spec: v1alpha1.SecurityGroupSpec{Ingress: []v1alpha1.SecurityGroupRule{{SecurityGroupRef: "web"}}}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "cache"}, Spec: v1alpha1.SecurityGroupSpec{Egress: []v1alpha1.SecurityGroupRule{{SecurityGroupRef: "web"}}}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "other"}},
	}
	got := ReferrersOfGroup(groups, "web")
	if len(got) != 2 || got[0] != request("ns", "db") || got[1] != request("ns", "cache") {
		t.Fatalf("requests %v", got)
	}
}

func TestGroupsOfVirtualMachineListsEveryGroup(t *testing.T) {
	vm := virtualMachineUsing("ns", "vm1", "web", "db")
	vm.Spec.Networks = append(vm.Spec.Networks, v1alpha1.VMNetwork{SubnetRef: "x", SecurityGroups: []string{"web", "cache"}})
	got := GroupsOfVirtualMachine(*vm)
	if len(got) != 3 {
		t.Fatalf("requests %v", got)
	}
}
