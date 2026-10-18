package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Facts struct {
	Hostname         string
	OSID             string
	OSVersionID      string
	Arch             string
	Root             bool
	Systemd          bool
	CgroupV2         bool
	KVM              bool
	NTPSynced        bool
	DefaultInterface string
	DefaultIP        string
	Interfaces       []string
	FreeVarLibBytes  uint64
	IOMMUGroups      int
}

type route struct {
	Dev string `json:"dev"`
}

type link struct {
	Name string `json:"ifname"`
}

type addrInfo struct {
	Family string `json:"family"`
	Local  string `json:"local"`
}

type addrEntry struct {
	AddrInfo []addrInfo `json:"addr_info"`
}

func Gather(ctx context.Context, e Exec, root string, freeBytes func(string) (uint64, error), uid int) (Facts, error) {
	id, version, err := ReadOSRelease(filepath.Join(root, "etc", "os-release"))
	if err != nil {
		return Facts{}, err
	}
	iface, err := defaultInterface(ctx, e)
	if err != nil {
		return Facts{}, err
	}
	ip, err := primaryAddress(ctx, e, iface)
	if err != nil {
		return Facts{}, err
	}
	links, err := interfaces(ctx, e)
	if err != nil {
		return Facts{}, err
	}
	ntp, err := e.Run(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
	if err != nil {
		return Facts{}, err
	}
	hostname, err := e.Run(ctx, "hostname")
	if err != nil {
		return Facts{}, err
	}
	free, err := freeBytes(filepath.Join(root, "var", "lib"))
	if err != nil {
		return Facts{}, err
	}
	return Facts{
		Hostname:         strings.ToLower(strings.TrimSpace(hostname)),
		OSID:             id,
		OSVersionID:      version,
		Arch:             runtime.GOARCH,
		Root:             uid == 0,
		Systemd:          exists(filepath.Join(root, "run", "systemd", "system")),
		CgroupV2:         exists(filepath.Join(root, "sys", "fs", "cgroup", "cgroup.controllers")),
		KVM:              exists(filepath.Join(root, "dev", "kvm")),
		NTPSynced:        strings.TrimSpace(ntp) == "yes",
		DefaultInterface: iface,
		DefaultIP:        ip,
		Interfaces:       links,
		FreeVarLibBytes:  free,
		IOMMUGroups:      countEntries(filepath.Join(root, "sys", "kernel", "iommu_groups")),
	}, nil
}

func countEntries(path string) int {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0
	}
	return len(entries)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func ReadOSRelease(path string) (id, versionID string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("read os-release: %w", err)
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		values[key] = strings.Trim(value, "\"'")
	}
	return values["ID"], values["VERSION_ID"], scanner.Err()
}

func defaultInterface(ctx context.Context, e Exec) (string, error) {
	out, err := e.Run(ctx, "ip", "-json", "route", "show", "default")
	if err != nil {
		return "", err
	}
	var routes []route
	if err := json.Unmarshal([]byte(out), &routes); err != nil {
		return "", fmt.Errorf("parse default route: %w", err)
	}
	if len(routes) == 0 || routes[0].Dev == "" {
		return "", fmt.Errorf("no default route")
	}
	return routes[0].Dev, nil
}

func primaryAddress(ctx context.Context, e Exec, iface string) (string, error) {
	out, err := e.Run(ctx, "ip", "-json", "-4", "addr", "show", "dev", iface)
	if err != nil {
		return "", err
	}
	var entries []addrEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return "", fmt.Errorf("parse addresses: %w", err)
	}
	for _, entry := range entries {
		for _, info := range entry.AddrInfo {
			if info.Family == "inet" && info.Local != "" {
				return info.Local, nil
			}
		}
	}
	return "", fmt.Errorf("no IPv4 address on %s", iface)
}

func HasAddress(ctx context.Context, e Exec, ip string) (bool, error) {
	out, err := e.Run(ctx, "ip", "-json", "addr")
	if err != nil {
		return false, err
	}
	var entries []addrEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return false, fmt.Errorf("parse addresses: %w", err)
	}
	for _, entry := range entries {
		for _, info := range entry.AddrInfo {
			if info.Local == ip {
				return true, nil
			}
		}
	}
	return false, nil
}

func interfaces(ctx context.Context, e Exec) ([]string, error) {
	out, err := e.Run(ctx, "ip", "-json", "link", "show")
	if err != nil {
		return nil, err
	}
	var links []link
	if err := json.Unmarshal([]byte(out), &links); err != nil {
		return nil, fmt.Errorf("parse links: %w", err)
	}
	names := make([]string, 0, len(links))
	for _, l := range links {
		names = append(names, l.Name)
	}
	return names, nil
}

func OSKey(id, versionID string) string {
	major, _, _ := strings.Cut(versionID, ".")
	switch id {
	case "rhel", "debian":
		return id + "-" + major
	case "centos":
		return "centos-stream-" + major
	case "sles":
		return "sles-" + major
	}
	return id + "-" + versionID
}
