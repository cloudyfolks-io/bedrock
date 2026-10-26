package hostconfig

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

const (
	legsUnitName = "bedrock-tenant-legs.service"
	legsTable    = "bedrock_legs"
)

var counterValues = regexp.MustCompile(`packets \d+ bytes \d+`)

var (
	legsRulesetPath = filepath.Join("etc", "bedrock", "tenant-legs.nft")
	legsAppliedPath = filepath.Join("etc", "bedrock", "tenant-legs.applied")
	legsUnitPath    = filepath.Join("etc", "systemd", "system", legsUnitName)
	legNamePattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
)

const legsUnit = `[Unit]
Description=drop host-bound traffic from the tenant-facing legs
DefaultDependencies=no
After=local-fs.target nftables.service
Before=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f /etc/bedrock/tenant-legs.nft

[Install]
WantedBy=multi-user.target
`

const legsRulesetTemplate = `table inet bedrock_legs
delete table inet bedrock_legs

table inet bedrock_legs {
  chain prerouting {
    type filter hook prerouting priority dstnat - 10; policy accept;
    iifname %[1]s ct state new fib daddr type local meta l4proto { tcp, udp } th dport 30000-32767 counter drop
  }

  chain input {
    type filter hook input priority filter - 10; policy accept;
    iifname %[1]s jump leg_input
  }

  chain forward {
    type filter hook forward priority filter - 10; policy accept;
    iifname %[1]s jump leg_forward
  }

  chain leg_forward {
    ct state established,related accept
    ct status dnat accept
    counter drop
  }

  chain leg_input {
    ct state established,related accept
%[2]s    icmp type { echo-request, destination-unreachable, time-exceeded } accept
    icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, packet-too-big, destination-unreachable, time-exceeded } accept
    counter drop
  }
}
`

func RenderLegsRuleset(legs []v1alpha1.TenantLegSpec) (string, error) {
	sorted := slices.SortedFunc(slices.Values(legs), func(a, b v1alpha1.TenantLegSpec) int {
		return strings.Compare(a.Interface, b.Interface)
	})
	names := make([]string, len(sorted))
	var sources strings.Builder
	for i, leg := range sorted {
		if !legNamePattern.MatchString(leg.Interface) {
			return "", fmt.Errorf("tenant leg interface %q is not a valid interface name", leg.Interface)
		}
		names[i] = strconv.Quote(leg.Interface)
		rules, err := sourceRules(leg)
		if err != nil {
			return "", err
		}
		sources.WriteString(rules)
	}
	return fmt.Sprintf(legsRulesetTemplate, "{ "+strings.Join(names, ", ")+" }", sources.String()), nil
}

func sourceRules(leg v1alpha1.TenantLegSpec) (string, error) {
	var v4, v6 []string
	for _, source := range slices.Sorted(slices.Values(leg.AllowSources)) {
		prefix, err := netip.ParsePrefix(source)
		if err != nil {
			return "", fmt.Errorf("tenant leg %s: allow source %q is not a CIDR", leg.Interface, source)
		}
		if prefix.Bits() == 0 {
			return "", fmt.Errorf("tenant leg %s: allow source %q matches every address", leg.Interface, source)
		}
		masked := prefix.Masked().String()
		if prefix.Addr().Is4() {
			v4 = append(v4, masked)
		} else {
			v6 = append(v6, masked)
		}
	}
	var b strings.Builder
	if len(v4) > 0 {
		fmt.Fprintf(&b, "    iifname %s ip saddr { %s } accept\n", strconv.Quote(leg.Interface), strings.Join(v4, ", "))
	}
	if len(v6) > 0 {
		fmt.Fprintf(&b, "    iifname %s ip6 saddr { %s } accept\n", strconv.Quote(leg.Interface), strings.Join(v6, ", "))
	}
	return b.String(), nil
}

func legsStep(ctx context.Context, deps Deps, spec v1alpha1.HostConfigSpec) (string, error) {
	if len(spec.TenantLegs) == 0 {
		return retireLegs(ctx, deps)
	}
	ruleset, err := RenderLegsRuleset(spec.TenantLegs)
	if err != nil {
		return "", err
	}
	rulesetReplaced, err := host.ReplaceFile(filepath.Join(deps.Root, legsRulesetPath), ruleset, 0o644)
	if err != nil {
		return "", err
	}
	unitReplaced, err := host.ReplaceFile(filepath.Join(deps.Root, legsUnitPath), legsUnit, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := deps.Exec.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return "", err
	}
	if err := enableLegsUnit(ctx, deps, rulesetReplaced || unitReplaced); err != nil {
		return "", err
	}
	return fmt.Sprintf("guarding %d tenant legs", len(spec.TenantLegs)), nil
}

func enableLegsUnit(ctx context.Context, deps Deps, replaced bool) error {
	if _, err := deps.Exec.Run(ctx, "systemctl", "enable", "--now", legsUnitName); err != nil {
		return err
	}
	if !replaced && tableIntact(ctx, deps) {
		return nil
	}
	return reloadTable(ctx, deps)
}

func GuardLegs(ctx context.Context, deps Deps, spec v1alpha1.HostConfigSpec) error {
	if len(spec.TenantLegs) == 0 || tableIntact(ctx, deps) {
		return nil
	}
	return reloadTable(ctx, deps)
}

func reloadTable(ctx context.Context, deps Deps) error {
	if _, err := deps.Exec.Run(ctx, "systemctl", "restart", legsUnitName); err != nil {
		return err
	}
	return recordLoadedTable(ctx, deps)
}

func loadedTable(ctx context.Context, deps Deps) (string, error) {
	listing, err := deps.Exec.Run(ctx, "nft", "list", "table", "inet", legsTable)
	if err != nil {
		return "", err
	}
	return counterValues.ReplaceAllString(listing, "packets 0 bytes 0"), nil
}

func tableIntact(ctx context.Context, deps Deps) bool {
	listing, err := loadedTable(ctx, deps)
	if err != nil {
		return false
	}
	recorded, err := os.ReadFile(filepath.Join(deps.Root, legsAppliedPath))
	return err == nil && string(recorded) == listing
}

func recordLoadedTable(ctx context.Context, deps Deps) error {
	listing, err := loadedTable(ctx, deps)
	if err != nil {
		return fmt.Errorf("table inet %s is not loaded after the start: %w", legsTable, err)
	}
	_, err = host.ReplaceFile(filepath.Join(deps.Root, legsAppliedPath), listing, 0o644)
	return err
}

func retireLegs(ctx context.Context, deps Deps) (string, error) {
	unit := filepath.Join(deps.Root, legsUnitPath)
	if _, err := os.Stat(unit); errors.Is(err, fs.ErrNotExist) {
		return "no tenant legs", nil
	}
	if _, err := deps.Exec.Run(ctx, "systemctl", "disable", "--now", legsUnitName); err != nil {
		return "", err
	}
	deps.Exec.Run(ctx, "nft", "delete", "table", "inet", legsTable)
	for _, path := range []string{unit, filepath.Join(deps.Root, legsRulesetPath), filepath.Join(deps.Root, legsAppliedPath)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	_, err := deps.Exec.Run(ctx, "systemctl", "daemon-reload")
	return "tenant leg guard removed", err
}
