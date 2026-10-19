package network

import (
	"maps"
	"net/netip"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

type SnatInput struct {
	Network     string
	EIP         string
	Labels      map[string]string
	Annotations map[string]string
}

func (in SnatInput) meta(scope string) fabric.Meta {
	return fabric.Meta{Name: SnatRuleName(in.Network, scope), Labels: maps.Clone(in.Labels), Annotations: maps.Clone(in.Annotations)}
}

func isIPv4Range(cidr string) bool {
	prefix, err := netip.ParsePrefix(cidr)
	return err == nil && prefix.Addr().Is4()
}

func RangeSnatRules(in SnatInput, ranges []string) []*unstructured.Unstructured {
	var rules []*unstructured.Unstructured
	for _, cidr := range ranges {
		if isIPv4Range(cidr) {
			rules = append(rules, fabric.OvnSnatRule(in.meta(cidr), in.EIP, in.Network, cidr))
		}
	}
	return rules
}

func SubnetSnatRule(in SnatInput, fabricSubnet string) *unstructured.Unstructured {
	return fabric.OvnSnatRuleForSubnet(in.meta(fabricSubnet), in.EIP, fabricSubnet)
}
