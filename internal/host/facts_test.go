package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const osRelease = "NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n"

func fakeRoot(t *testing.T, kvm bool) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"etc", "sys/fs/cgroup", "dev", "run/systemd/system"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte(osRelease), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sys", "fs", "cgroup", "cgroup.controllers"), []byte("cpu memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if kvm {
		if err := os.WriteFile(filepath.Join(root, "dev", "kvm"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func fakeIP() *FakeExec {
	return &FakeExec{Responses: map[string]string{
		"ip -json route show default":                 `[{"dst":"default","gateway":"10.0.10.1","dev":"bond0.10","protocol":"static"}]`,
		"ip -json -4 addr show dev bond0.10":          `[{"ifname":"bond0.10","addr_info":[{"family":"inet","local":"10.0.10.11","prefixlen":24}]}]`,
		"ip -json link show":                          `[{"ifname":"lo"},{"ifname":"bond0"},{"ifname":"bond0.10"}]`,
		"timedatectl show -p NTPSynchronized --value": "yes\n",
		"hostname": "host-01\n",
	}}
}

func TestGather(t *testing.T) {
	root := fakeRoot(t, true)
	facts, err := Gather(context.Background(), fakeIP(), root, func(string) (uint64, error) { return 50 << 30, nil }, 0)
	if err != nil {
		t.Fatal(err)
	}
	if facts.OSID != "ubuntu" || facts.OSVersionID != "24.04" || !facts.Root || !facts.Systemd || !facts.CgroupV2 || !facts.KVM || !facts.NTPSynced {
		t.Fatalf("facts %+v", facts)
	}
	if facts.DefaultInterface != "bond0.10" || facts.DefaultIP != "10.0.10.11" || len(facts.Interfaces) != 3 || facts.FreeVarLibBytes != 50<<30 || facts.Hostname != "host-01" {
		t.Fatalf("facts %+v", facts)
	}
}

func TestGatherLowersTheHostname(t *testing.T) {
	root := fakeRoot(t, true)
	e := fakeIP()
	e.Responses["hostname"] = "Host-01.Example\n"
	facts, err := Gather(context.Background(), e, root, func(string) (uint64, error) { return 1, nil }, 0)
	if err != nil || facts.Hostname != "host-01.example" {
		t.Fatalf("hostname %q err %v", facts.Hostname, err)
	}
}

func TestGatherWithoutKVMAndNotRoot(t *testing.T) {
	root := fakeRoot(t, false)
	facts, err := Gather(context.Background(), fakeIP(), root, func(string) (uint64, error) { return 1, nil }, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if facts.KVM || facts.Root {
		t.Fatalf("facts %+v", facts)
	}
}

func TestGatherCountsIOMMUGroups(t *testing.T) {
	root := fakeRoot(t, true)
	for _, group := range []string{"0", "1"} {
		if err := os.MkdirAll(filepath.Join(root, "sys", "kernel", "iommu_groups", group), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := Gather(context.Background(), fakeIP(), root, func(string) (uint64, error) { return 50 << 30, nil }, 0)
	if err != nil {
		t.Fatal(err)
	}
	if facts.IOMMUGroups != 2 {
		t.Fatalf("iommu groups %d", facts.IOMMUGroups)
	}
}

func TestGatherWithoutIOMMU(t *testing.T) {
	root := fakeRoot(t, true)
	facts, err := Gather(context.Background(), fakeIP(), root, func(string) (uint64, error) { return 50 << 30, nil }, 0)
	if err != nil {
		t.Fatal(err)
	}
	if facts.IOMMUGroups != 0 {
		t.Fatalf("iommu groups %d", facts.IOMMUGroups)
	}
}

func TestOSKey(t *testing.T) {
	cases := map[[2]string]string{
		{"ubuntu", "24.04"}: "ubuntu-24.04",
		{"debian", "13"}:    "debian-13",
		{"fedora", "43"}:    "fedora-43",
		{"rhel", "9.4"}:     "rhel-9",
		{"centos", "10"}:    "centos-stream-10",
		{"sles", "16.0"}:    "sles-16",
		{"arch", ""}:        "arch-",
	}
	for in, want := range cases {
		if got := OSKey(in[0], in[1]); got != want {
			t.Fatalf("OSKey(%q,%q)=%q want %q", in[0], in[1], got, want)
		}
	}
}
