package preflight

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

const minFreeBytes = 20 << 30

type Result struct {
	Name    string
	Fatal   bool
	OK      bool
	Message string
}

func Run(ctx context.Context, e host.Exec, facts host.Facts, devices []host.Device, cfg v1alpha1.ClusterConfig, supportedOS []string) []Result {
	osKey := host.OSKey(facts.OSID, facts.OSVersionID)
	results := []Result{
		check("root", true, facts.Root, "run as root"),
		check("systemd", true, facts.Systemd, "systemd is required"),
		check("arch", true, facts.Arch == "amd64" || facts.Arch == "arm64", fmt.Sprintf("unsupported architecture %s", facts.Arch)),
		check("os", true, slices.Contains(supportedOS, osKey), fmt.Sprintf("%s is not in the supported list %v", osKey, supportedOS)),
		check("cgroup-v2", true, facts.CgroupV2, "cgroup v2 is required"),
		check("kvm", false, facts.KVM, "/dev/kvm not found, virtual machines will not start on this host"),
		check("ntp", false, facts.NTPSynced, "clock is not NTP synchronized"),
		check("disk", false, facts.FreeVarLibBytes >= minFreeBytes, fmt.Sprintf("%d GiB free on /var/lib, at least 20 GiB recommended", facts.FreeVarLibBytes>>30)),
		check("management-interface", true, slices.Contains(facts.Interfaces, cfg.Spec.Network.ManagementInterface), fmt.Sprintf("interface %s not found", cfg.Spec.Network.ManagementInterface)),
		vipCheck(cfg.Spec.API.VIP, facts.DefaultIP),
		vipFreeCheck(ctx, e, cfg.Spec.API.VIP),
		check("iommu", false, facts.IOMMUGroups > 0, "no IOMMU groups found, hardware passthrough will not work"),
	}
	for _, device := range devices {
		results = append(results, deviceCheck(device))
	}
	return results
}

func check(name string, fatal, ok bool, message string) Result {
	if ok {
		return Result{Name: name, Fatal: fatal, OK: true}
	}
	return Result{Name: name, Fatal: fatal, Message: message}
}

func vipCheck(vip, nodeIP string) Result {
	addr, err := netip.ParseAddr(vip)
	if err != nil || !addr.Is4() {
		return Result{Name: "vip", Fatal: true, Message: fmt.Sprintf("vip %q is not an IPv4 address", vip)}
	}
	if vip == nodeIP {
		return Result{Name: "vip", Fatal: true, Message: "vip must differ from the node address"}
	}
	return Result{Name: "vip", Fatal: true, OK: true}
}

func vipFreeCheck(ctx context.Context, e host.Exec, vip string) Result {
	assigned, err := host.HasAddress(ctx, e, vip)
	if err != nil {
		return Result{Name: "vip-free", Fatal: true, Message: err.Error()}
	}
	if assigned {
		return Result{Name: "vip-free", Fatal: true, OK: true}
	}
	_, err = e.Run(ctx, "ping", "-c", "1", "-W", "1", vip)
	var exit *host.ExitError
	switch {
	case err == nil:
		return Result{Name: "vip-free", Fatal: true, Message: fmt.Sprintf("vip %s already answers to ping, choose a free address", vip)}
	case errors.As(err, &exit):
		return Result{Name: "vip-free", Fatal: true, OK: true}
	default:
		return Result{Name: "vip-free", Fatal: true, Message: fmt.Sprintf("cannot probe vip %s: %v", vip, err)}
	}
}

func deviceCheck(device host.Device) Result {
	name := "device-" + device.Path
	if !device.Block {
		return Result{Name: name, Fatal: true, Message: "not a block device"}
	}
	if device.Signature != "" {
		return Result{Name: name, Fatal: true, Message: fmt.Sprintf("has a %s signature, wipe it first", device.Signature)}
	}
	return Result{Name: name, Fatal: true, OK: true}
}

func Blocked(results []Result) bool {
	for _, r := range results {
		if r.Fatal && !r.OK {
			return true
		}
	}
	return false
}

func Format(results []Result) string {
	var b strings.Builder
	for _, r := range results {
		switch {
		case r.OK:
			fmt.Fprintf(&b, "[ok] %s\n", r.Name)
		case r.Fatal:
			fmt.Fprintf(&b, "[fail] %s: %s\n", r.Name, r.Message)
		default:
			fmt.Fprintf(&b, "[warn] %s: %s\n", r.Name, r.Message)
		}
	}
	return b.String()
}
