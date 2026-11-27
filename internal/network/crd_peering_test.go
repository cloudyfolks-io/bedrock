package network

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func peeringSpec(name, connect string, refs ...string) *v1alpha1.NetworkPeering {
	return &v1alpha1.NetworkPeering{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.NetworkPeeringSpec{NetworkRefs: refs, ConnectCIDR: connect}}
}

func TestNetworkPeeringNeedsTwoDifferentNetworks(t *testing.T) {
	c := cleanEnv(t)
	requireAccepted(t, c, peeringSpec("ok", "", "a", "b"))
	requireRejected(t, c, peeringSpec("one", "", "a"), "networkRefs")
	requireRejected(t, c, peeringSpec("three", "", "a", "b", "c"), "networkRefs")
	requireRejected(t, c, peeringSpec("self", "", "a", "a"), "a Network cannot peer with itself")
}

func TestNetworkPeeringConnectCIDRMustBeACanonicalSlash30(t *testing.T) {
	c := cleanEnv(t)
	requireAccepted(t, c, peeringSpec("ok", "169.254.0.0/30", "a", "b"))
	for name, cidr := range map[string]string{"hostbits": "169.254.0.1/30", "wide": "169.254.0.0/29", "narrow": "169.254.0.0/31", "ipv6": "fd00::/126"} {
		requireRejected(t, c, peeringSpec(name, cidr, "a", "b"), "connectCIDR must be a canonical IPv4 network with a prefix of 30")
	}
}

func TestNetworkPeeringSpecIsImmutable(t *testing.T) {
	c := cleanEnv(t)
	peering := peeringSpec("fixed", "169.254.0.0/30", "a", "b")
	requireAccepted(t, c, peering)
	peering.Spec.NetworkRefs = []string{"a", "c"}
	if err := c.Update(context.Background(), peering); err == nil {
		t.Fatal("networkRefs must be immutable")
	}
	stored := getPeering(t, c, "fixed")
	peering = &stored
	peering.Spec.ConnectCIDR = "169.254.0.4/30"
	if err := c.Update(context.Background(), peering); err == nil {
		t.Fatal("connectCIDR must be immutable")
	}
	open := peeringSpec("open", "", "a", "b")
	requireAccepted(t, c, open)
	open.Spec.ConnectCIDR = "169.254.0.4/30"
	if err := c.Update(context.Background(), open); err == nil {
		t.Fatal("connectCIDR must not be added later")
	}
}

func TestNetworkStaticRoutesMustBeCanonicalIPv4WithAnIPv4NextHop(t *testing.T) {
	c := cleanEnv(t)
	good := networkOf(unique(t, "net"), "acme", "10.100.0.0/16")
	good.Spec.StaticRoutes = []v1alpha1.NetworkStaticRoute{{CIDR: "10.50.0.0/16", NextHop: "10.100.0.9"}}
	requireAccepted(t, c, good)
	cases := map[string]v1alpha1.NetworkStaticRoute{
		"hostbits": {CIDR: "10.50.0.1/16", NextHop: "10.100.0.9"},
		"default":  {CIDR: "0.0.0.0/0", NextHop: "10.100.0.9"},
		"ipv6":     {CIDR: "fd00::/64", NextHop: "10.100.0.9"},
		"nexthop":  {CIDR: "10.50.0.0/16", NextHop: "not-an-ip"},
		"v6hop":    {CIDR: "10.50.0.0/16", NextHop: "fd00::1"},
	}
	for name, route := range cases {
		network := networkOf(unique(t, "net"), "acme", "10.100.0.0/16")
		network.Spec.StaticRoutes = []v1alpha1.NetworkStaticRoute{route}
		t.Run(name, func(t *testing.T) { requireRejected(t, c, network, "") })
	}
}

func TestNetworkStaticRoutesAreKeyedByCIDR(t *testing.T) {
	c := cleanEnv(t)
	network := networkOf(unique(t, "net"), "acme", "10.100.0.0/16")
	network.Spec.StaticRoutes = []v1alpha1.NetworkStaticRoute{{CIDR: "10.50.0.0/16", NextHop: "10.100.0.9"}, {CIDR: "10.50.0.0/16", NextHop: "10.100.0.8"}}
	requireRejected(t, c, network, "")
}
