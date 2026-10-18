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

func TestAdoptPoolNameProblem(t *testing.T) {
	cases := []struct {
		name  string
		pools AdoptPools
		want  string
	}{
		{"valid names", AdoptPools{Block: "storage-block", Filesystem: "filesystem-data", ObjectStore: "object.store_1"}, ""},
		{"empty names", AdoptPools{}, ""},
		{"space in block", AdoptPools{Block: "has space"}, "storage.adopt.pools.block is not a valid Ceph name"},
		{"slash in filesystem", AdoptPools{Filesystem: "slash/name"}, "storage.adopt.pools.filesystem is not a valid Ceph name"},
		{"leading dash in object store", AdoptPools{ObjectStore: "-leading"}, "storage.adopt.pools.objectStore is not a valid Ceph name"},
		{"shell text", AdoptPools{ObjectStore: "$(id)"}, "storage.adopt.pools.objectStore is not a valid Ceph name"},
		{"semicolon", AdoptPools{Block: "a;b"}, "storage.adopt.pools.block is not a valid Ceph name"},
		{"first problem wins", AdoptPools{Block: "ok", Filesystem: "bad name", ObjectStore: "worse name"}, "storage.adopt.pools.filesystem is not a valid Ceph name"},
	}
	for _, tc := range cases {
		if got := AdoptPoolNameProblem(tc.pools); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
