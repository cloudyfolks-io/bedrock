package network

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func subnetOf(namespace, name, cidr string) *v1alpha1.Subnet {
	return &v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.SubnetSpec{NetworkRef: "net", CIDR: cidr, DHCP: true},
	}
}

func networkOf(name, tenant, cidr string) *v1alpha1.Network {
	return &v1alpha1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.NetworkSpec{TenantRef: tenant, CIDR: cidr},
	}
}

func requireAccepted(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	if err := c.Create(context.Background(), obj); err != nil {
		t.Fatalf("%T %s must be accepted: %v", obj, obj.GetName(), err)
	}
}

func requireRejected(t *testing.T, c client.Client, obj client.Object, fragment string) {
	t.Helper()
	err := c.Create(context.Background(), obj)
	if err == nil {
		t.Fatalf("%T %s must be rejected", obj, obj.GetName())
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("error %q does not contain %q", err, fragment)
	}
}

func TestSubnetCIDRMustBeCanonicalIPv4Between16And28(t *testing.T) {
	c, _ := testEnv(t)
	ns := newNamespace(t, "crd", unique(t, "p"))
	requireAccepted(t, c, subnetOf(ns, "wide", "10.100.0.0/16"))
	requireAccepted(t, c, subnetOf(ns, "narrow", "10.100.1.0/28"))
	for name, cidr := range map[string]string{"hostbits": "10.100.1.5/24", "toowide": "10.100.0.0/15", "toonarrow": "10.100.1.0/29", "ipv6": "fd00::/64"} {
		requireRejected(t, c, subnetOf(ns, name, cidr), "cidr must be an IPv4 network address with a prefix from 16 to 28")
	}
}

func TestSubnetGatewayMustBeInsideTheCIDR(t *testing.T) {
	c, _ := testEnv(t)
	ns := newNamespace(t, "crd", unique(t, "p"))
	good := subnetOf(ns, "good", "10.100.1.0/24")
	good.Spec.Gateway = "10.100.1.1"
	requireAccepted(t, c, good)
	requireAccepted(t, c, subnetOf(ns, "nogateway", "10.100.3.0/24"))
	outside := subnetOf(ns, "outside", "10.100.1.0/24")
	outside.Spec.Gateway = "10.100.2.1"
	requireRejected(t, c, outside, "gateway must be an address inside cidr")
	garbage := subnetOf(ns, "garbage", "10.100.1.0/24")
	garbage.Spec.Gateway = "not-an-ip"
	requireRejected(t, c, garbage, "gateway")
}

func TestSubnetNameMustBeADNSLabel(t *testing.T) {
	c, _ := testEnv(t)
	ns := newNamespace(t, "crd", unique(t, "p"))
	requireAccepted(t, c, subnetOf(ns, "default", "10.100.1.0/24"))
	requireRejected(t, c, subnetOf(ns, "a.b", "10.100.2.0/24"), "")
	requireRejected(t, c, subnetOf(ns, strings.Repeat("a", 64), "10.100.2.0/24"), "")
}

func TestSubnetNetworkRefAndCIDRAreImmutable(t *testing.T) {
	c, _ := testEnv(t)
	ns := newNamespace(t, "crd", unique(t, "p"))
	subnet := subnetOf(ns, "fixed", "10.100.1.0/24")
	requireAccepted(t, c, subnet)
	changed := subnet.DeepCopy()
	changed.Spec.NetworkRef = "other"
	if err := c.Update(context.Background(), changed); err == nil {
		t.Fatal("networkRef must be immutable")
	}
	changed = subnet.DeepCopy()
	changed.Spec.CIDR = "10.100.2.0/24"
	if err := c.Update(context.Background(), changed); err == nil {
		t.Fatal("cidr must be immutable")
	}
}

func elasticIPOf(namespace, name, address string) *v1alpha1.ElasticIP {
	return &v1alpha1.ElasticIP{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.ElasticIPSpec{ProviderNetwork: "public", Address: address},
	}
}

func TestElasticIPProviderNetworkAndAddressAreImmutable(t *testing.T) {
	c, _ := testEnv(t)
	ctx := context.Background()
	ns := newNamespace(t, "crd", unique(t, "p"))
	fixed := elasticIPOf(ns, "fixed", "192.0.2.50")
	requireAccepted(t, c, fixed)
	floating := elasticIPOf(ns, "floating", "")
	requireAccepted(t, c, floating)

	moved := fixed.DeepCopy()
	moved.Spec.ProviderNetwork = "backup"
	if err := c.Update(ctx, moved); err == nil {
		t.Fatal("providerNetwork must be immutable")
	}
	changed := fixed.DeepCopy()
	changed.Spec.Address = "192.0.2.51"
	if err := c.Update(ctx, changed); err == nil {
		t.Fatal("address must be immutable")
	}
	late := floating.DeepCopy()
	late.Spec.Address = "192.0.2.52"
	if err := c.Update(ctx, late); err == nil {
		t.Fatal("address must not be set after create")
	}
	same := fixed.DeepCopy()
	same.Spec.Address = "192.0.2.50"
	same.Labels = map[string]string{"k": "v"}
	if err := c.Update(ctx, same); err != nil {
		t.Fatalf("an unchanged update must be accepted: %v", err)
	}
}

func TestSecurityGroupNameMustBeADNSLabel(t *testing.T) {
	c, _ := testEnv(t)
	ns := newNamespace(t, "crd", unique(t, "p"))
	group := func(name string) *v1alpha1.SecurityGroup {
		return &v1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	}
	requireAccepted(t, c, group("web"))
	requireRejected(t, c, group("a.b"), "the name must be a DNS label")
	requireRejected(t, c, group(strings.Repeat("a", 64)), "")
}

func TestNetworkNameIsAtMost30Characters(t *testing.T) {
	c, _ := testEnv(t)
	requireAccepted(t, c, networkOf(strings.Repeat("a", 30), "acme", "10.100.0.0/16"))
	requireRejected(t, c, networkOf(strings.Repeat("b", 31), "acme", "10.101.0.0/16"), "the name must be a DNS label of at most 30 characters")
}

func TestNetworkNameIsNotReservedByFabric(t *testing.T) {
	c, _ := testEnv(t)
	requireRejected(t, c, networkOf("provider-public", "acme", "10.102.0.0/16"), "the name is reserved for the Fabric underlay")
	requireRejected(t, c, networkOf("ovn-cluster", "acme", "10.103.0.0/16"), "the name is reserved for the Fabric underlay")
}

func TestNetworkStatusSubnetsRoundTrip(t *testing.T) {
	c, _ := testEnv(t)
	ctx := context.Background()
	network := networkOf(unique(t, "st"), "acme", "10.100.0.0/16")
	requireAccepted(t, c, network)
	network.Status.Subnets = []v1alpha1.NetworkSubnetStatus{{Namespace: "p1", Name: "a", CIDR: "10.100.1.0/24"}, {Namespace: "p1", Name: "b", CIDR: "10.100.2.0/24"}}
	if err := c.Status().Update(ctx, network); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.Network
	if err := c.Get(ctx, client.ObjectKeyFromObject(network), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Subnets) != 2 || got.Status.Subnets[1].CIDR != "10.100.2.0/24" {
		t.Fatalf("status %+v", got.Status.Subnets)
	}
	duplicate := got.DeepCopy()
	duplicate.Status.Subnets = []v1alpha1.NetworkSubnetStatus{{Namespace: "p1", Name: "a", CIDR: "10.100.1.0/24"}, {Namespace: "p1", Name: "a", CIDR: "10.100.9.0/24"}}
	if err := c.Status().Update(ctx, duplicate); err == nil {
		t.Fatal("a duplicate list key must be refused")
	}
	crowded := got.DeepCopy()
	for i := 0; i <= 512; i++ {
		crowded.Status.Subnets = append(crowded.Status.Subnets, v1alpha1.NetworkSubnetStatus{Namespace: "p1", Name: unique(t, "s"), CIDR: "10.100.1.0/24"})
	}
	if err := c.Status().Update(ctx, crowded); err == nil {
		t.Fatal("more than 512 entries must be refused")
	}
}
