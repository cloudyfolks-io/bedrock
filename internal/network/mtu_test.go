package network

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestMTUViolation(t *testing.T) {
	cases := []struct {
		name          string
		mtu, underlay int
		refused       bool
	}{
		{"1500 on 1500 underlay", 1500, 1500, true},
		{"1400 on 1500 underlay", 1400, 1500, false},
		{"8900 on 9000 underlay", 8900, 9000, false},
		{"9000 on 9000 underlay", 9000, 9000, true},
		{"unknown underlay", 9000, 0, false},
		{"unset MTU", 0, 1500, false},
	}
	for _, c := range cases {
		v := MTUViolation(c.mtu, c.underlay)
		if (v != nil) != c.refused {
			t.Errorf("%s: violation = %v, want refused %v", c.name, v, c.refused)
		}
		if v != nil && v.Reason != ReasonMTUExceedsUnderlay {
			t.Errorf("%s: reason = %s", c.name, v.Reason)
		}
	}
}

func nodeAndHost(name, address string, links ...v1alpha1.LinkInfo) (corev1.Node, v1alpha1.Host) {
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: address}}
	host := v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}}
	host.Status.Inventory.Links = links
	return node, host
}

func TestUnderlayMTUIsTheTunnelInterfaceNotOtherNICs(t *testing.T) {
	node, host := nodeAndHost("a", "10.0.10.5",
		v1alpha1.LinkInfo{Name: "eno2", MTU: 1500, Addresses: []string{"192.168.0.5"}},
		v1alpha1.LinkInfo{Name: "bond0.10", MTU: 9000, Addresses: []string{"10.0.10.5"}},
	)
	underlay := UnderlayMTU([]corev1.Node{node}, []v1alpha1.Host{host})
	if underlay != 9000 {
		t.Fatalf("UnderlayMTU = %d, want 9000", underlay)
	}
	if v := MTUViolation(1500, underlay); v != nil {
		t.Fatalf("1500 on a 9000 tunnel interface refused: %v", v)
	}
}

func TestUnderlayMTUIsTheSmallestTunnelInterface(t *testing.T) {
	nodeA, hostA := nodeAndHost("a", "10.0.0.1", v1alpha1.LinkInfo{Name: "bond0", MTU: 9000, Addresses: []string{"10.0.0.1"}})
	nodeB, hostB := nodeAndHost("b", "10.0.0.2", v1alpha1.LinkInfo{Name: "eth0", MTU: 1500, Addresses: []string{"10.0.0.2"}})
	underlay := UnderlayMTU([]corev1.Node{nodeA, nodeB}, []v1alpha1.Host{hostA, hostB})
	if underlay != 1500 {
		t.Fatalf("UnderlayMTU = %d, want 1500", underlay)
	}
	if v := MTUViolation(1500, underlay); v == nil {
		t.Fatal("1500 on a 1500 tunnel interface accepted")
	}
}

func TestUnderlayMTUIsUnknownWhenOneNodeHasNoTunnelLink(t *testing.T) {
	nodeA, hostA := nodeAndHost("a", "10.0.0.1", v1alpha1.LinkInfo{Name: "eth0", MTU: 1500, Addresses: []string{"10.0.0.1"}})
	nodeB, hostB := nodeAndHost("b", "10.0.0.2")
	if got := UnderlayMTU([]corev1.Node{nodeA, nodeB}, []v1alpha1.Host{hostA, hostB}); got != 0 {
		t.Fatalf("UnderlayMTU = %d, want 0", got)
	}
	if got := UnderlayMTU(nil, nil); got != 0 {
		t.Fatalf("UnderlayMTU of no nodes = %d, want 0", got)
	}
}
