package network

import (
	"testing"

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

func TestUnderlayMTUIsTheSmallestLinkedNIC(t *testing.T) {
	host := func(nics ...v1alpha1.NICInfo) v1alpha1.Host {
		var h v1alpha1.Host
		h.Status.Inventory.NICs = nics
		return h
	}
	hosts := []v1alpha1.Host{
		host(v1alpha1.NICInfo{Name: "a", Link: true, MTU: 9000}, v1alpha1.NICInfo{Name: "b", Link: false, MTU: 1500}),
		host(v1alpha1.NICInfo{Name: "a", Link: true, MTU: 1500}, v1alpha1.NICInfo{Name: "c", Link: true}),
	}
	if got := UnderlayMTU(hosts); got != 1500 {
		t.Fatalf("UnderlayMTU = %d, want 1500", got)
	}
	if got := UnderlayMTU(nil); got != 0 {
		t.Fatalf("UnderlayMTU of no hosts = %d, want 0", got)
	}
}
