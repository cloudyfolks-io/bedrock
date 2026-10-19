package network

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	servicesIdentitySuffix = "-services-snat"
	servicesACLPriority    = 2000
	servicesACLDirection   = "to-lport"
	servicesACLAction      = "drop"
	servicesVIPPrefixBits  = 24
)

var servicesSnatCIDRs = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

type Services struct {
	ProviderNetwork string
	IdentityRange   string
	Resolver        string
}

type ServicesObjects struct {
	Port     *unstructured.Unstructured
	Identity *unstructured.Unstructured
	Rules    []*unstructured.Unstructured
}

func (s ServicesObjects) All() []*unstructured.Unstructured {
	return slices.Concat([]*unstructured.Unstructured{s.Port, s.Identity}, s.Rules)
}

func (s ServicesObjects) Guarded() []*unstructured.Unstructured {
	return slices.Concat([]*unstructured.Unstructured{s.Identity}, s.Rules)
}

func MissingServicesSettings(c Config) []string {
	s := c.Services
	if s.ProviderNetwork == "" {
		return nil
	}
	var missing []string
	pool, found := ProviderOf(c, s.ProviderNetwork)
	switch {
	case !found:
		missing = append(missing, keyServicesProviderNetwork+"="+s.ProviderNetwork+" (not a provider network)")
	case pool.Role != v1alpha1.ProviderRoleServices:
		missing = append(missing, keyServicesProviderNetwork+"="+s.ProviderNetwork+" (its role is not services)")
	}
	switch {
	case s.IdentityRange == "":
		missing = append(missing, keyServicesIdentityRange)
	case found && !insidePool(s.IdentityRange, pool):
		missing = append(missing, keyServicesIdentityRange+"="+s.IdentityRange+" (not inside the cidr of the provider network)")
	case found && !identityExcluded(s.IdentityRange, pool):
		missing = append(missing, keyServicesIdentityRange+"="+s.IdentityRange+" (not inside one excludeIPs entry of the provider network)")
	}
	return missing
}

func insidePool(cidr string, pool v1alpha1.ProviderNetworkSpec) bool {
	prefix, err := netip.ParsePrefix(cidr)
	return err == nil && insideAny(prefix, []string{pool.CIDR})
}

func identityExcluded(cidr string, pool v1alpha1.ProviderNetworkSpec) bool {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false
	}
	first := prefix.Addr().Next()
	last := lastAddress(prefix).Prev()
	return slices.ContainsFunc(pool.ExcludeIPs, func(entry string) bool {
		low, high, err := settings.ParseAddressRange(entry)
		return err == nil && low.Compare(first) <= 0 && high.Compare(last) >= 0
	})
}

func ServicesSubnet(c Config) (string, bool) {
	if c.Services.ProviderNetwork == "" || len(MissingServicesSettings(c)) > 0 {
		return "", false
	}
	return ExternalSubnetName(c.Services.ProviderNetwork), true
}

func isServicesPool(pn v1alpha1.ProviderNetworkSpec) bool {
	return pn.Role == v1alpha1.ProviderRoleServices
}

func ServicesIdentityName(vpc string) string {
	return vpc + servicesIdentitySuffix
}

func ServicesRuleName(vpc string, index int) string {
	return ServicesIdentityName(vpc) + "-" + strconv.Itoa(index)
}

func ServicesPortName(vpc, subnet string) string {
	return vpc + "-" + subnet
}

func NextServicesIdentity(rangeCIDR string, used []string) (string, error) {
	prefix, err := netip.ParsePrefix(rangeCIDR)
	if err != nil {
		return "", fmt.Errorf("network.services.identity-range %q is not a network", rangeCIDR)
	}
	taken := make(map[string]bool, len(used))
	for _, address := range used {
		taken[address] = true
	}
	for addr := prefix.Addr().Next(); prefix.Contains(addr.Next()); addr = addr.Next() {
		if !taken[addr.String()] {
			return addr.String(), nil
		}
	}
	return "", fmt.Errorf("every address of network.services.identity-range %s is in use", rangeCIDR)
}

func ServicesPlane(network v1alpha1.Network, cfg Config, identity string) ServicesObjects {
	subnet := ExternalSubnetName(cfg.Services.ProviderNetwork)
	meta := func(name string) fabric.Meta {
		return fabric.Meta{Name: name, Labels: ObjectLabels("Network", network.Name, network.Spec.TenantRef, ""), Annotations: OwnerAnnotations(&network)}
	}
	vpc := VpcName(network.Name)
	rules := make([]*unstructured.Unstructured, len(servicesSnatCIDRs))
	for i, cidr := range servicesSnatCIDRs {
		rules[i] = fabric.OvnSnatRule(meta(ServicesRuleName(vpc, i)), ServicesIdentityName(vpc), vpc, cidr)
	}
	return ServicesObjects{
		Port:     fabric.OvnEipLRP(meta(ServicesPortName(vpc, subnet)), subnet),
		Identity: fabric.OvnEip(meta(ServicesIdentityName(vpc)), subnet, identity),
		Rules:    rules,
	}
}

func servicesACLs(cfg Config, pn v1alpha1.ProviderNetworkSpec) []fabric.ACL {
	if !isServicesPool(pn) {
		return nil
	}
	pool, err := netip.ParsePrefix(pn.CIDR)
	if err != nil {
		return nil
	}
	vips := pool
	if pool.Bits() < servicesVIPPrefixBits {
		vips = netip.PrefixFrom(pool.Addr(), servicesVIPPrefixBits)
	}
	match := fmt.Sprintf("ip4.src == %[1]s && ip4.src != %[2]s && ip4.dst == %[1]s && ip4.dst != %[2]s", pool, vips)
	return []fabric.ACL{{Direction: servicesACLDirection, Priority: servicesACLPriority, Match: match, Action: servicesACLAction}}
}
