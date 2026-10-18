package preflight

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

const (
	adoptFSID = "7e1b4a3c-5d2f-4c8e-9a61-0b3f2d8c4e57"
	otherFSID = "11111111-2222-3333-4444-555555555555"
	diskA     = "/dev/disk/by-id/wwn-0x1"
	diskB     = "/dev/disk/by-id/wwn-0x2"
)

func adoptSpec() v1alpha1.AdoptSpec {
	return v1alpha1.AdoptSpec{
		FSID:     adoptFSID,
		KeyFile:  "/root/rook-ceph-mon.yaml",
		Monitors: []v1alpha1.AdoptMonitor{{ID: "a", Node: "node-1", Address: "10.0.10.11"}, {ID: "b", Node: "node-2", Address: "10.0.10.12"}},
		Hosts: []v1alpha1.AdoptHost{
			{Node: "node-1", Devices: []string{diskA, diskB}},
			{Node: "node-2", Devices: []string{"/dev/disk/by-id/wwn-0x3"}},
			{Node: "node-3", Devices: []string{"/dev/disk/by-id/wwn-0x4"}},
		},
	}
}

func adoptConfig() v1alpha1.ClusterConfig {
	cfg := goodConfig()
	cfg.Spec.Storage.Devices = nil
	adopt := adoptSpec()
	cfg.Spec.Storage.Adopt = &adopt
	return cfg
}

func goodAdoptFacts() host.AdoptFacts {
	return host.AdoptFacts{MonStores: map[string]bool{"a": true}, MonAddresses: map[string]bool{"a": true}, ConfigFSID: adoptFSID, ListeningPorts: []int{22, 10250}}
}

func goodFactsOn(hostname string) host.Facts {
	facts := goodFacts()
	facts.Hostname = hostname
	return facts
}

func runWithDevices(cfg v1alpha1.ClusterConfig, devices ...host.Device) []Result {
	return Run(context.Background(), goodExec(), goodFacts(), devices, cfg, []string{"ubuntu-24.04"})
}

func resultNamed(t *testing.T, results []Result, name string) Result {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no result %s in\n%s", name, Format(results))
	return Result{}
}

func TestRunAdoptAcceptsDisksOfTheAdoptedCluster(t *testing.T) {
	devices := []host.Device{
		{Path: diskA, Block: true, Signature: "ceph_bluestore", CephFSID: adoptFSID},
		{Path: diskB, Block: true, Signature: "LVM2_member", CephFSID: adoptFSID},
	}
	results := runWithDevices(adoptConfig(), devices...)
	if Blocked(results) {
		t.Fatalf("adopted disks must pass:\n%s", Format(results))
	}
	for _, path := range []string{diskA, diskB} {
		if r := resultNamed(t, results, "device-"+path); !r.OK || !r.Fatal {
			t.Fatalf("device %s: %+v", path, r)
		}
	}
}

func TestRunAdoptRefusesOtherDisks(t *testing.T) {
	cases := map[string]struct {
		device host.Device
		want   string
	}{
		"bluestore of another cluster": {host.Device{Path: diskA, Block: true, Signature: "ceph_bluestore", CephFSID: otherFSID}, "Ceph label fsid " + otherFSID + " does not match storage.adopt.fsid"},
		"lvm of another cluster":       {host.Device{Path: diskA, Block: true, Signature: "LVM2_member", CephFSID: otherFSID}, "Ceph label fsid " + otherFSID + " does not match storage.adopt.fsid"},
		"bluestore without fsid":       {host.Device{Path: diskA, Block: true, Signature: "ceph_bluestore"}, "has a ceph_bluestore signature, wipe it first"},
		"lvm without ceph tags":        {host.Device{Path: diskA, Block: true, Signature: "LVM2_member"}, "has a LVM2_member signature, wipe it first"},
		"filesystem":                   {host.Device{Path: diskA, Block: true, Signature: "ext4"}, "has a ext4 signature, wipe it first"},
		"filesystem with a fsid":       {host.Device{Path: diskA, Block: true, Signature: "xfs", CephFSID: adoptFSID}, "has a xfs signature, wipe it first"},
		"encrypted":                    {host.Device{Path: diskA, Block: true, Signature: "crypto_LUKS"}, "has a crypto_LUKS signature, wipe it first"},
		"not a block device":           {host.Device{Path: diskA}, "not a block device"},
	}
	for name, tc := range cases {
		results := runWithDevices(adoptConfig(), tc.device)
		r := resultNamed(t, results, "device-"+diskA)
		if r.OK || !r.Fatal || r.Message != tc.want {
			t.Fatalf("%s: %+v", name, r)
		}
		if !Blocked(results) {
			t.Fatalf("%s: must block", name)
		}
	}
}

func TestRunAdoptAcceptsBlankDisk(t *testing.T) {
	results := runWithDevices(adoptConfig(), host.Device{Path: diskA, Block: true})
	if Blocked(results) {
		t.Fatalf("a blank disk must pass:\n%s", Format(results))
	}
}

func TestRunWithoutAdoptRefusesCephDisks(t *testing.T) {
	cfg := goodConfig()
	for _, device := range []host.Device{
		{Path: "/dev/sdb", Block: true, Signature: "ceph_bluestore", CephFSID: adoptFSID},
		{Path: "/dev/sdb", Block: true, Signature: "LVM2_member", CephFSID: adoptFSID},
	} {
		results := runWithDevices(cfg, device)
		r := resultNamed(t, results, "device-/dev/sdb")
		if r.OK || r.Message != "has a "+device.Signature+" signature, wipe it first" {
			t.Fatalf("%+v", r)
		}
	}
}

func TestFormatShowsTheForeignFSID(t *testing.T) {
	results := runWithDevices(adoptConfig(), host.Device{Path: diskA, Block: true, Signature: "ceph_bluestore", CephFSID: otherFSID})
	want := "[fail] device-" + diskA + ": Ceph label fsid " + otherFSID + " does not match storage.adopt.fsid"
	if !strings.Contains(Format(results), want) {
		t.Fatalf("format\n%s", Format(results))
	}
}

func TestAdoptChecksAllGood(t *testing.T) {
	results := AdoptChecks(adoptSpec(), goodFactsOn("node-1"), goodAdoptFacts())
	for _, name := range []string{"adopt-node", "adopt-k0s", "adopt-mon-a-store", "adopt-mon-a-address", "adopt-mon-a-config", "adopt-mon-a-port-3300", "adopt-mon-a-port-6789"} {
		if r := resultNamed(t, results, name); !r.OK || !r.Fatal {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	if len(results) != 7 || Blocked(results) {
		t.Fatalf("results\n%s", Format(results))
	}
}

func TestAdoptChecksNodeWithoutMonitor(t *testing.T) {
	results := AdoptChecks(adoptSpec(), goodFactsOn("node-3"), host.AdoptFacts{})
	if len(results) != 2 || Blocked(results) {
		t.Fatalf("results\n%s", Format(results))
	}
}

func TestAdoptChecksUnknownNode(t *testing.T) {
	results := AdoptChecks(adoptSpec(), goodFactsOn("node-9"), host.AdoptFacts{})
	r := resultNamed(t, results, "adopt-node")
	if r.OK || !r.Fatal || r.Message != "this host is node-9, storage.adopt.hosts names no such node" {
		t.Fatalf("%+v", r)
	}
	if len(results) != 1 {
		t.Fatalf("no other check can run for an unknown node:\n%s", Format(results))
	}
}

func TestAdoptChecksMonitorProblems(t *testing.T) {
	cases := map[string]struct {
		facts host.AdoptFacts
		name  string
		want  string
	}{
		"store missing":     {host.AdoptFacts{MonStores: map[string]bool{}, ConfigFSID: adoptFSID}, "adopt-mon-a-store", "/var/lib/rook/mon-a/data/store.db not found"},
		"config missing":    {host.AdoptFacts{MonStores: map[string]bool{"a": true}}, "adopt-mon-a-config", "/var/lib/rook/rook-ceph/rook-ceph.config has no fsid"},
		"config other fsid": {host.AdoptFacts{MonStores: map[string]bool{"a": true}, ConfigFSID: otherFSID}, "adopt-mon-a-config", "/var/lib/rook/rook-ceph/rook-ceph.config has fsid " + otherFSID + ", storage.adopt.fsid is " + adoptFSID},
		"3300 busy":         {host.AdoptFacts{MonStores: map[string]bool{"a": true}, ConfigFSID: adoptFSID, ListeningPorts: []int{3300}}, "adopt-mon-a-port-3300", "TCP port 3300 is in use, stop the old monitor first"},
		"6789 busy":         {host.AdoptFacts{MonStores: map[string]bool{"a": true}, ConfigFSID: adoptFSID, ListeningPorts: []int{22, 6789}}, "adopt-mon-a-port-6789", "TCP port 6789 is in use, stop the old monitor first"},
	}
	for name, tc := range cases {
		results := AdoptChecks(adoptSpec(), goodFactsOn("node-1"), tc.facts)
		r := resultNamed(t, results, tc.name)
		if r.OK || !r.Fatal || r.Message != tc.want {
			t.Fatalf("%s: %+v", name, r)
		}
		if !Blocked(results) {
			t.Fatalf("%s: must block", name)
		}
	}
}

func TestAdoptChecksSkipPortsWhenABedrockK0sRuns(t *testing.T) {
	facts := host.AdoptFacts{MonStores: map[string]bool{"a": true}, MonAddresses: map[string]bool{"a": true}, ConfigFSID: adoptFSID, ListeningPorts: []int{3300, 6789}, K0sRunning: true, BedrockInstalled: true}
	results := AdoptChecks(adoptSpec(), goodFactsOn("node-1"), facts)
	if Blocked(results) || len(results) != 5 {
		t.Fatalf("results\n%s", Format(results))
	}
	for _, name := range []string{"adopt-node", "adopt-k0s", "adopt-mon-a-store", "adopt-mon-a-address", "adopt-mon-a-config"} {
		if r := resultNamed(t, results, name); !r.OK {
			t.Fatalf("%s: %+v", name, r)
		}
	}
}

func TestAdoptChecksKeepStoreAndConfigWhenK0sRuns(t *testing.T) {
	cases := map[string]struct {
		facts host.AdoptFacts
		name  string
	}{
		"store missing": {host.AdoptFacts{MonStores: map[string]bool{}, ConfigFSID: adoptFSID, K0sRunning: true, BedrockInstalled: true}, "adopt-mon-a-store"},
		"other fsid":    {host.AdoptFacts{MonStores: map[string]bool{"a": true}, ConfigFSID: otherFSID, K0sRunning: true, BedrockInstalled: true}, "adopt-mon-a-config"},
	}
	for name, tc := range cases {
		results := AdoptChecks(adoptSpec(), goodFactsOn("node-1"), tc.facts)
		if r := resultNamed(t, results, tc.name); r.OK || !r.Fatal {
			t.Fatalf("%s: %+v", name, r)
		}
	}
}

func TestAdoptChecksRefuseAK0sThatIsNoBedrockInstall(t *testing.T) {
	facts := host.AdoptFacts{MonStores: map[string]bool{"a": true}, ConfigFSID: adoptFSID, ListeningPorts: []int{3300, 6789}, K0sRunning: true}
	results := AdoptChecks(adoptSpec(), goodFactsOn("node-1"), facts)
	r := resultNamed(t, results, "adopt-k0s")
	if r.OK || !r.Fatal || r.Message != "k0s runs but this host is no Bedrock install; stop and reset the old cluster first" {
		t.Fatalf("%+v", r)
	}
	if !Blocked(results) {
		t.Fatal("must block")
	}
	for _, name := range []string{"adopt-mon-a-port-3300", "adopt-mon-a-port-6789"} {
		if r := resultNamed(t, results, name); r.OK || !r.Fatal {
			t.Fatalf("a foreign k0s must keep the port check %s: %+v", name, r)
		}
	}
}

func TestAdoptChecksAcceptAHostWithoutK0s(t *testing.T) {
	results := AdoptChecks(adoptSpec(), goodFactsOn("node-3"), host.AdoptFacts{BedrockInstalled: true})
	if r := resultNamed(t, results, "adopt-k0s"); !r.OK {
		t.Fatalf("%+v", r)
	}
}

func TestAdoptChecksRefuseAForeignK0sOnAHostWithoutMonitor(t *testing.T) {
	results := AdoptChecks(adoptSpec(), goodFactsOn("node-3"), host.AdoptFacts{K0sRunning: true})
	if r := resultNamed(t, results, "adopt-k0s"); r.OK || !r.Fatal {
		t.Fatalf("%+v", r)
	}
}

func TestAdoptChecksRefuseAMonitorAddressThatIsNotOnThisHost(t *testing.T) {
	facts := goodAdoptFacts()
	facts.MonAddresses = map[string]bool{"a": false}
	results := AdoptChecks(adoptSpec(), goodFactsOn("node-1"), facts)
	r := resultNamed(t, results, "adopt-mon-a-address")
	if r.OK || !r.Fatal || r.Message != "monitor address 10.0.10.11 is not an address of this host" {
		t.Fatalf("%+v", r)
	}
	if !Blocked(results) {
		t.Fatal("must block")
	}
}

func TestAdoptChecksKeepTheAddressCheckWhenABedrockK0sRuns(t *testing.T) {
	facts := host.AdoptFacts{MonStores: map[string]bool{"a": true}, ConfigFSID: adoptFSID, K0sRunning: true, BedrockInstalled: true}
	results := AdoptChecks(adoptSpec(), goodFactsOn("node-1"), facts)
	if r := resultNamed(t, results, "adopt-mon-a-address"); r.OK || !r.Fatal {
		t.Fatalf("%+v", r)
	}
}

func TestRunAdoptWarnsBelow100GiBFreeOnVarLib(t *testing.T) {
	facts := goodFacts()
	facts.FreeVarLibBytes = 60 << 30
	results := Run(context.Background(), goodExec(), facts, nil, adoptConfig(), []string{"ubuntu-24.04"})
	r := resultNamed(t, results, "disk")
	if r.OK || r.Fatal || r.Message != "60 GiB free on /var/lib, at least 100 GiB recommended" {
		t.Fatalf("%+v", r)
	}
	facts.FreeVarLibBytes = 100 << 30
	results = Run(context.Background(), goodExec(), facts, nil, adoptConfig(), []string{"ubuntu-24.04"})
	if r := resultNamed(t, results, "disk"); !r.OK {
		t.Fatalf("%+v", r)
	}
}

func TestRunWithoutAdoptKeepsTheTwentyGiBDiskWarning(t *testing.T) {
	facts := goodFacts()
	facts.FreeVarLibBytes = 60 << 30
	results := Run(context.Background(), goodExec(), facts, nil, goodConfig(), []string{"ubuntu-24.04"})
	if r := resultNamed(t, results, "disk"); !r.OK {
		t.Fatalf("%+v", r)
	}
	facts.FreeVarLibBytes = 10 << 30
	results = Run(context.Background(), goodExec(), facts, nil, goodConfig(), []string{"ubuntu-24.04"})
	r := resultNamed(t, results, "disk")
	if r.OK || r.Message != "10 GiB free on /var/lib, at least 20 GiB recommended" {
		t.Fatalf("%+v", r)
	}
}
