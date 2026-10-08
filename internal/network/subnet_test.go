package network

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func subnetInput(gateway string, dns []string) SubnetInput {
	return SubnetInput{
		Subnet: v1alpha1.Subnet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "t1-prod", Name: "default", UID: types.UID("uid-1")},
			Spec:       v1alpha1.SubnetSpec{NetworkRef: "t1", CIDR: "10.100.1.0/24", Gateway: gateway, DHCP: true, ExcludeIPs: []string{"10.100.1.2..10.100.1.5"}},
		},
		Network:    v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "t1"}},
		Tenant:     "t1",
		Project:    "prod",
		DNSServers: dns,
	}
}

func TestDesiredFabricSubnet(t *testing.T) {
	u := DesiredFabricSubnet(subnetInput("10.100.1.1", []string{"1.1.1.1", "8.8.8.8"}))
	if u.GetName() != FabricName("t1-prod", "default") {
		t.Fatal(u.GetName())
	}
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	want := map[string]any{
		"vpc": "t1", "protocol": "IPv4", "cidrBlock": "10.100.1.0/24", "gateway": "10.100.1.1",
		"excludeIps": []any{"10.100.1.2..10.100.1.5"}, "provider": "default.t1-prod.fabric",
		"gatewayType": "distributed", "natOutgoing": false, "private": false, "disableGatewayCheck": true,
		"enableDHCP": true, "dhcpV4Options": "dns_server=1.1.1.1;8.8.8.8",
	}
	for k, v := range want {
		if got := spec[k]; !equalValue(got, v) {
			t.Fatalf("spec[%s] = %v, want %v", k, got, v)
		}
	}
	if len(spec) != len(want) {
		t.Fatalf("spec %v", spec)
	}
	labels := u.GetLabels()
	if labels[v1alpha1.LabelKind] != "Subnet" || labels[v1alpha1.LabelName] != "default" || labels[v1alpha1.LabelTenant] != "t1" || labels[v1alpha1.LabelProject] != "prod" || labels[LabelManaged] != "true" {
		t.Fatalf("labels %v", labels)
	}
	key, uid, ok := OwnerOf(u)
	if !ok || key != (types.NamespacedName{Namespace: "t1-prod", Name: "default"}) || uid != "uid-1" {
		t.Fatalf("owner %v %v", key, uid)
	}
}

func equalValue(a, b any) bool {
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok && bok {
		return slices.Equal(as, bs)
	}
	return a == b
}

func TestDesiredFabricSubnetDefaultsTheGateway(t *testing.T) {
	u := DesiredFabricSubnet(subnetInput("", nil))
	if gateway, _, _ := unstructured.NestedString(u.Object, "spec", "gateway"); gateway != "10.100.1.1" {
		t.Fatalf("gateway %q", gateway)
	}
	if _, found, _ := unstructured.NestedString(u.Object, "spec", "dhcpV4Options"); found {
		t.Fatal("no DNS servers means no DHCP options")
	}
}

func TestDesiredFabricSubnetWithoutDHCP(t *testing.T) {
	in := subnetInput("", []string{"1.1.1.1"})
	in.Subnet.Spec.DHCP = false
	spec, _, _ := unstructured.NestedMap(DesiredFabricSubnet(in).Object, "spec")
	if _, found := spec["enableDHCP"]; found {
		t.Fatalf("spec %v", spec)
	}
}

func TestDesiredAttachment(t *testing.T) {
	u := DesiredAttachment(subnetInput("", nil))
	if u.GetKind() != "NetworkAttachmentDefinition" || u.GetAPIVersion() != "k8s.cni.cncf.io/v1" || u.GetNamespace() != "t1-prod" || u.GetName() != "default" {
		t.Fatalf("identity %v", u.Object)
	}
	if config, _, _ := unstructured.NestedString(u.Object, "spec", "config"); config != AttachmentConfig("t1-prod", "default") {
		t.Fatalf("config %q", config)
	}
	refs := u.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "Subnet" || refs[0].UID != "uid-1" || refs[0].Controller == nil || !*refs[0].Controller || refs[0].BlockOwnerDeletion == nil || !*refs[0].BlockOwnerDeletion {
		t.Fatalf("owner references %+v", refs)
	}
	if u.GetLabels()[LabelManaged] != "true" {
		t.Fatalf("labels %v", u.GetLabels())
	}
}

func TestDHCPv4OptionsJoinsServersWithSemicolons(t *testing.T) {
	if got := DHCPv4Options([]string{"1.1.1.1", "8.8.8.8"}); got != "dns_server=1.1.1.1;8.8.8.8" {
		t.Fatal(got)
	}
	if DHCPv4Options(nil) != "" {
		t.Fatal("no servers, no options")
	}
}
