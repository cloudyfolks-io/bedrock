package inventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

type lscpuEntry struct {
	Field    string       `json:"field"`
	Data     string       `json:"data"`
	Children []lscpuEntry `json:"children"`
}

type lscpuOutput struct {
	Entries []lscpuEntry `json:"lscpu"`
}

func lscpuFields(entries []lscpuEntry, fields map[string]string) map[string]string {
	for _, entry := range entries {
		fields[entry.Field] = entry.Data
		lscpuFields(entry.Children, fields)
	}
	return fields
}

func ParseLscpu(raw []byte) (v1alpha1.CPUInfo, error) {
	var out lscpuOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return v1alpha1.CPUInfo{}, fmt.Errorf("lscpu: %w", err)
	}
	fields := lscpuFields(out.Entries, map[string]string{})
	if fields["Model name:"] == "" {
		return v1alpha1.CPUInfo{}, fmt.Errorf("lscpu: Model name not found")
	}
	perCore := atoi(fields["Thread(s) per core:"])
	sockets := atoi(fields["Socket(s):"])
	cores := lscpuCores(fields, sockets)
	return v1alpha1.CPUInfo{Model: fields["Model name:"], Cores: cores, Threads: perCore * cores, Sockets: sockets}, nil
}

func lscpuCores(fields map[string]string, sockets int32) int32 {
	if perSocket, ok := fields["Core(s) per socket:"]; ok {
		return atoi(perSocket) * sockets
	}
	clusters := atoi(fields["Cluster(s):"])
	return atoi(fields["Core(s) per cluster:"]) * clusters
}

func atoi(s string) int32 {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return int32(n)
}

func ParseMeminfo(text string) (int64, error) {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("meminfo: %w", err)
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("meminfo: MemTotal not found")
}

type lsblkOutput struct {
	Devices []struct {
		Path        string    `json:"path"`
		Model       *string   `json:"model"`
		Serial      *string   `json:"serial"`
		Size        int64     `json:"size"`
		Rota        bool      `json:"rota"`
		WWN         *string   `json:"wwn"`
		Type        string    `json:"type"`
		Mountpoints []*string `json:"mountpoints"`
		FSType      *string   `json:"fstype"`
	} `json:"blockdevices"`
}

func ParseLsblk(raw []byte) ([]v1alpha1.DiskInfo, error) {
	var out lsblkOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	disks := make([]v1alpha1.DiskInfo, 0, len(out.Devices))
	for _, dev := range out.Devices {
		if dev.Type != "disk" {
			continue
		}
		disks = append(disks, v1alpha1.DiskInfo{Path: dev.Path, Model: deref(dev.Model), Serial: deref(dev.Serial), SizeBytes: dev.Size, Rotational: dev.Rota, WWN: deref(dev.WWN), InUseBy: inUseBy(dev.Mountpoints, deref(dev.FSType))})
	}
	return disks, nil
}

func inUseBy(mountpoints []*string, fstype string) string {
	for _, mp := range mountpoints {
		if mp != nil && *mp != "" {
			return "mounted"
		}
	}
	return fstype
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type ipLink struct {
	Name      string `json:"ifname"`
	LinkType  string `json:"link_type"`
	OperState string `json:"operstate"`
	Address   string `json:"address"`
	LinkInfo  *struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
}

func ParseIPLink(raw []byte, sysRoot string) ([]v1alpha1.NICInfo, error) {
	var links []ipLink
	if err := json.Unmarshal(raw, &links); err != nil {
		return nil, fmt.Errorf("ip link: %w", err)
	}
	nics := make([]v1alpha1.NICInfo, 0, len(links))
	for _, link := range links {
		if link.LinkType != "ether" || link.Name == "lo" || link.LinkInfo != nil {
			continue
		}
		dev := filepath.Join(sysRoot, "class", "net", link.Name)
		nics = append(nics, v1alpha1.NICInfo{Name: link.Name, MAC: link.Address, Link: link.OperState == "UP", SpeedMbps: readSpeed(filepath.Join(dev, "speed")), PCIAddress: linkBase(filepath.Join(dev, "device")), Driver: linkBase(filepath.Join(dev, "device", "driver"))})
	}
	return nics, nil
}

func readSpeed(path string) int32 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := atoi(string(raw))
	if n < 0 {
		return 0
	}
	return n
}

func linkBase(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

func ParseLspci(text string, sysRoot string) ([]v1alpha1.PCIInfo, error) {
	var devices []v1alpha1.PCIInfo
	for _, block := range strings.Split(strings.TrimSpace(text), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok {
				fields[key] = strings.TrimSpace(value)
			}
		}
		slot := fields["Slot"]
		if slot == "" {
			continue
		}
		devices = append(devices, v1alpha1.PCIInfo{Address: slot, Vendor: fields["Vendor"], Device: fields["Device"], Class: fields["Class"], Driver: fields["Driver"], IOMMUGroup: atoi(linkBase(filepath.Join(sysRoot, "bus", "pci", "devices", slot, "iommu_group")))})
	}
	if len(devices) == 0 && strings.TrimSpace(text) != "" {
		return nil, fmt.Errorf("lspci: no devices parsed")
	}
	return devices, nil
}
