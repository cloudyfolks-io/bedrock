package inventory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseLscpu(t *testing.T) {
	cpu, err := ParseLscpu(fixture(t, "lscpu.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cpu.Model != "AMD EPYC 7302P 16-Core Processor" || cpu.Sockets != 1 || cpu.Cores != 16 || cpu.Threads != 32 {
		t.Fatalf("cpu %+v", cpu)
	}
}

func TestParseLscpuNested(t *testing.T) {
	flat, err := ParseLscpu(fixture(t, "lscpu.json"))
	if err != nil {
		t.Fatal(err)
	}
	nested, err := ParseLscpu(fixture(t, "lscpu-nested.json"))
	if err != nil {
		t.Fatal(err)
	}
	if nested != flat {
		t.Fatalf("nested %+v flat %+v", nested, flat)
	}
}

func TestParseLscpuArm64Clusters(t *testing.T) {
	cpu, err := ParseLscpu(fixture(t, "lscpu-arm64.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cpu.Model != "-" || cpu.Sockets != 1 || cpu.Cores != 6 || cpu.Threads != 6 {
		t.Fatalf("cpu %+v", cpu)
	}
}

func TestParseLscpuFailsWithoutModel(t *testing.T) {
	if _, err := ParseLscpu([]byte(`{"lscpu": [{"field": "Socket(s):", "data": "1"}]}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseMeminfo(t *testing.T) {
	got, err := ParseMeminfo("MemTotal:       65780032 kB\nMemFree:        1234 kB\n")
	if err != nil || got != 65780032*1024 {
		t.Fatalf("got %d err %v", got, err)
	}
	if _, err := ParseMeminfo("nothing"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseLsblkKeepsDisksOnly(t *testing.T) {
	disks, err := ParseLsblk(fixture(t, "lsblk.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 2 {
		t.Fatalf("disks %+v", disks)
	}
	if disks[0].Path != "/dev/nvme0n1" || disks[0].SizeBytes != 960197124096 || disks[0].Rotational || disks[0].InUseBy != "mounted" || disks[0].Serial != "S4EVNX0R" {
		t.Fatalf("disk0 %+v", disks[0])
	}
	if disks[1].Path != "/dev/sda" || !disks[1].Rotational || disks[1].InUseBy != "" || disks[1].WWN != "0x5000c500b1c2d3e4" {
		t.Fatalf("disk1 %+v", disks[1])
	}
}

func TestParseIPLinkReadsSysfs(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "sys", "class", "net", "eth0")
	os.MkdirAll(filepath.Join(root, "sys", "bus", "pci", "devices", "0000:01:00.0"), 0o755)
	os.MkdirAll(filepath.Join(root, "sys", "bus", "pci", "drivers", "ixgbe"), 0o755)
	os.MkdirAll(dev, 0o755)
	os.WriteFile(filepath.Join(dev, "speed"), []byte("10000\n"), 0o644)
	os.Symlink("../../../bus/pci/devices/0000:01:00.0", filepath.Join(dev, "device"))
	os.Symlink("../../../drivers/ixgbe", filepath.Join(root, "sys", "bus", "pci", "devices", "0000:01:00.0", "driver"))
	nics, err := ParseIPLink(fixture(t, "iplink.json"), filepath.Join(root, "sys"))
	if err != nil {
		t.Fatal(err)
	}
	if len(nics) != 1 {
		t.Fatalf("nics %+v", nics)
	}
	n := nics[0]
	if n.Name != "eth0" || n.MAC != "3c:ec:ef:12:34:56" || !n.Link || n.SpeedMbps != 10000 || n.PCIAddress != "0000:01:00.0" || n.Driver != "ixgbe" {
		t.Fatalf("nic %+v", n)
	}
}

func TestParseLspci(t *testing.T) {
	root := t.TempDir()
	group := filepath.Join(root, "sys", "kernel", "iommu_groups", "12")
	os.MkdirAll(group, 0o755)
	devDir := filepath.Join(root, "sys", "bus", "pci", "devices", "0000:01:00.0")
	os.MkdirAll(devDir, 0o755)
	os.Symlink("../../../../kernel/iommu_groups/12", filepath.Join(devDir, "iommu_group"))
	pci, err := ParseLspci(string(fixture(t, "lspci.txt")), filepath.Join(root, "sys"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pci) != 2 {
		t.Fatalf("pci %+v", pci)
	}
	if pci[0].Address != "0000:01:00.0" || pci[0].Vendor != "Intel Corporation [8086]" || pci[0].Class != "Ethernet controller [0200]" || pci[0].Driver != "ixgbe" || pci[0].IOMMUGroup != 12 {
		t.Fatalf("pci0 %+v", pci[0])
	}
	if pci[1].IOMMUGroup != 0 || pci[1].Driver != "" {
		t.Fatalf("pci1 %+v", pci[1])
	}
}

func TestGatherUsesExecAndRoot(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "proc"), 0o755)
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	os.MkdirAll(filepath.Join(root, "sys", "class", "dmi", "id"), 0o755)
	os.WriteFile(filepath.Join(root, "proc", "meminfo"), []byte("MemTotal:       1024 kB\n"), 0o644)
	os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "sys", "class", "dmi", "id", "bios_version"), []byte("2.7\n"), 0o644)
	exec := &host.FakeExec{Responses: map[string]string{
		"lscpu -J": string(fixture(t, "lscpu.json")),
		"lsblk -J -b -d -o NAME,PATH,MODEL,SERIAL,SIZE,ROTA,WWN,TYPE,MOUNTPOINTS,FSTYPE": string(fixture(t, "lsblk.json")),
		"ip -j link show": string(fixture(t, "iplink.json")),
		"lspci -Dvmmnn":   string(fixture(t, "lspci.txt")),
		"uname -r":        "6.8.0-45-generic\n",
	}}
	inv, err := Gather(context.Background(), exec, root)
	if err != nil {
		t.Fatal(err)
	}
	if inv.MemoryBytes != 1024*1024 || inv.CPU.Cores != 16 || len(inv.Disks) != 2 || len(inv.NICs) != 1 || len(inv.PCI) != 2 {
		t.Fatalf("inventory %+v", inv)
	}
	if inv.Firmware.BIOS != "2.7" || inv.Firmware.Kernel != "6.8.0-45-generic" || inv.Firmware.OS != "ubuntu" || inv.Firmware.OSVersion != "24.04" {
		t.Fatalf("firmware %+v", inv.Firmware)
	}
}

func TestGatherFailsWhenLscpuMissing(t *testing.T) {
	exec := &host.FakeExec{Errors: map[string]error{"lscpu -J": &host.ExitError{Code: 127}}, Responses: map[string]string{"lscpu -J": ""}}
	if _, err := Gather(context.Background(), exec, t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
}
