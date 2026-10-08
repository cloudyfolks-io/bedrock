package network

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const (
	apexName      = "@"
	maxDNSNameLen = 253
)

type Record struct {
	Name string
	IPs  []string
}

func invalidRecord(format string, args ...any) *Violation {
	return &Violation{Reason: ReasonInvalidRecords, Message: fmt.Sprintf(format, args...)}
}

func qualifiedName(zone, name string) (string, *Violation) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	switch {
	case name == apexName:
		return zone, nil
	case name == zone || strings.HasSuffix(name, "."+zone):
		return name, validName(name)
	case name != "" && !strings.Contains(name, "."):
		return name + "." + zone, validName(name + "." + zone)
	}
	return "", invalidRecord("the record name %q is not inside the zone %q; use a single label, @ or a full name in the zone", name, zone)
}

func validName(name string) *Violation {
	if len(name) > maxDNSNameLen {
		return invalidRecord("the record name %q is too long", name)
	}
	for _, label := range strings.Split(name, ".") {
		if len(validation.IsDNS1123Label(label)) > 0 {
			return invalidRecord("the record name %q has an invalid label %q", name, label)
		}
	}
	return nil
}

func FabricRecords(zone string, records []Record) ([]fabric.DNSRecord, *Violation) {
	zone = strings.ToLower(zone)
	merged := map[string][]string{}
	for _, record := range records {
		name, v := qualifiedName(zone, record.Name)
		if v != nil {
			return nil, v
		}
		if len(record.IPs) == 0 {
			return nil, invalidRecord("the record %q has no address", record.Name)
		}
		for _, ip := range record.IPs {
			if _, err := netip.ParseAddr(ip); err != nil {
				return nil, invalidRecord("the address %q of the record %q is not valid", ip, record.Name)
			}
			merged[name] = append(merged[name], ip)
		}
	}
	out := make([]fabric.DNSRecord, 0, len(merged))
	for name, ips := range merged {
		slices.Sort(ips)
		out = append(out, fabric.DNSRecord{Name: name, IPs: slices.Compact(ips)})
	}
	slices.SortFunc(out, func(a, b fabric.DNSRecord) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func NetworksOf(namespace, zone string, subnets []v1alpha1.Subnet) []string {
	var networks []string
	for _, s := range subnets {
		if s.Namespace == namespace && s.Spec.DNSZoneRef == zone {
			networks = append(networks, s.Spec.NetworkRef)
		}
	}
	slices.Sort(networks)
	return slices.Compact(networks)
}
