package v1alpha1

import (
	"slices"
	"testing"
)

func TestAdoptHostDevices(t *testing.T) {
	adopt := &AdoptSpec{Hosts: []AdoptHost{
		{Node: "node-1", Devices: []string{"/dev/disk/by-id/wwn-0x1", "/dev/disk/by-id/wwn-0x2"}},
		{Node: "node-2"},
	}}
	cases := map[string]struct {
		adopt *AdoptSpec
		node  string
		want  []string
	}{
		"listed node":            {adopt, "node-1", []string{"/dev/disk/by-id/wwn-0x1", "/dev/disk/by-id/wwn-0x2"}},
		"listed without devices": {adopt, "node-2", nil},
		"unlisted node":          {adopt, "node-9", nil},
		"no adopt":               {nil, "node-1", nil},
	}
	for name, tc := range cases {
		if got := AdoptHostDevices(tc.adopt, tc.node); !slices.Equal(got, tc.want) {
			t.Fatalf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestAdoptHostDevicesReturnsACopy(t *testing.T) {
	adopt := &AdoptSpec{Hosts: []AdoptHost{{Node: "node-1", Devices: []string{"/dev/disk/by-id/wwn-0x1"}}}}
	AdoptHostDevices(adopt, "node-1")[0] = "changed"
	if adopt.Hosts[0].Devices[0] != "/dev/disk/by-id/wwn-0x1" {
		t.Fatal("the result shares memory with the spec")
	}
}
