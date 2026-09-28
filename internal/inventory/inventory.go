package inventory

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func Gather(ctx context.Context, e host.Exec, root string) (v1alpha1.Inventory, error) {
	lscpu, err := e.Run(ctx, "lscpu", "-J")
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	cpu, err := ParseLscpu([]byte(lscpu))
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	meminfo, err := os.ReadFile(filepath.Join(root, "proc", "meminfo"))
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	memory, err := ParseMeminfo(string(meminfo))
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	lsblk, err := e.Run(ctx, "lsblk", "-J", "-b", "-d", "-o", "NAME,PATH,MODEL,SERIAL,SIZE,ROTA,WWN,TYPE,MOUNTPOINTS,FSTYPE")
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	disks, err := ParseLsblk([]byte(lsblk))
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	sysRoot := filepath.Join(root, "sys")
	links, err := e.Run(ctx, "ip", "-j", "link", "show")
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	nics, err := ParseIPLink([]byte(links), sysRoot)
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	lspci, err := e.Run(ctx, "lspci", "-Dvmmnn")
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	pci, err := ParseLspci(lspci, sysRoot)
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	kernel, err := e.Run(ctx, "uname", "-r")
	if err != nil {
		return v1alpha1.Inventory{}, err
	}
	return v1alpha1.Inventory{CPU: cpu, MemoryBytes: memory, Disks: disks, NICs: nics, PCI: pci, Firmware: Firmware(root, strings.TrimSpace(kernel))}, nil
}

func Firmware(root string, kernel string) v1alpha1.FirmwareInfo {
	bios, _ := os.ReadFile(filepath.Join(root, "sys", "class", "dmi", "id", "bios_version"))
	id, version, _ := host.ReadOSRelease(filepath.Join(root, "etc", "os-release"))
	return v1alpha1.FirmwareInfo{BIOS: strings.TrimSpace(string(bios)), Kernel: kernel, OS: id, OSVersion: version}
}
