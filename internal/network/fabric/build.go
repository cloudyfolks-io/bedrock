package fabric

import (
	"maps"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	AnnotationServiceHealthCheck = "fabric.cloudyfolks.io/service_health_check"
	LabelExternalGateway         = "fabric.cloudyfolks.io/external-gw"
)

type Meta struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
}

type StaticRoute struct{ CIDR, NextHop string }

type DynamicRouting struct {
	VrfID          uint32
	ExternalSubnet string
	Redistribute   []string
}

type VpcSpec struct {
	EnableExternal       bool
	ExtraExternalSubnets []string
	DefaultRoute         *StaticRoute
	Dynamic              *DynamicRouting
}

type SubnetSpec struct {
	Vpc, CIDR, Gateway, Provider, Vlan, DHCPOptions string
	ExcludeIPs                                      []string
	DHCP                                            bool
	Overlay                                         bool
}

type RouterLBPort struct {
	Name             string
	Port, TargetPort int32
	Protocol         string
}

type DNSRecord struct {
	Name string
	IPs  []string
}

type SGRule struct {
	IPVersion, Protocol, RemoteType, RemoteAddress, RemoteSecurityGroup, Policy string
	Priority, PortMin, PortMax                                                  int
}

type BgpPeer struct {
	Address string
	ASN     uint32
}

func object(kind schema.GroupVersionKind, m Meta, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(kind)
	u.SetName(m.Name)
	if len(m.Labels) > 0 {
		u.SetLabels(maps.Clone(m.Labels))
	}
	if len(m.Annotations) > 0 {
		u.SetAnnotations(maps.Clone(m.Annotations))
	}
	return u
}

func anys(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func selector(labels map[string]string) map[string]any {
	match := make(map[string]any, len(labels))
	for k, v := range labels {
		match[k] = v
	}
	return map[string]any{"matchLabels": match}
}

func Vpc(m Meta, s VpcSpec) *unstructured.Unstructured {
	spec := map[string]any{}
	if s.EnableExternal {
		spec["enableExternal"] = true
		spec["extraExternalSubnets"] = anys(s.ExtraExternalSubnets)
	}
	if s.DefaultRoute != nil {
		spec["staticRoutes"] = []any{map[string]any{"policy": "policyDst", "cidr": s.DefaultRoute.CIDR, "nextHopIP": s.DefaultRoute.NextHop}}
	}
	if s.Dynamic != nil {
		spec["dynamicRouting"] = map[string]any{
			"enabled":        true,
			"redistribute":   anys(s.Dynamic.Redistribute),
			"vrfId":          int64(s.Dynamic.VrfID),
			"externalSubnet": s.Dynamic.ExternalSubnet,
		}
	}
	return object(VpcGVK, m, spec)
}

func Subnet(m Meta, s SubnetSpec) *unstructured.Unstructured {
	spec := map[string]any{"protocol": "IPv4", "cidrBlock": s.CIDR, "gateway": s.Gateway}
	set(spec, "vpc", s.Vpc)
	set(spec, "dhcpV4Options", s.DHCPOptions)
	if len(s.ExcludeIPs) > 0 {
		spec["excludeIps"] = anys(s.ExcludeIPs)
	}
	if s.DHCP {
		spec["enableDHCP"] = true
	}
	if !s.Overlay {
		set(spec, "vlan", s.Vlan)
		set(spec, "provider", s.Provider)
	}
	if !s.Overlay && s.Provider != "" {
		spec["gatewayType"] = "distributed"
		spec["natOutgoing"] = false
		spec["private"] = false
		spec["disableGatewayCheck"] = true
	}
	return object(SubnetGVK, m, spec)
}

func set(spec map[string]any, key, value string) {
	if value != "" {
		spec[key] = value
	}
}

func OvnEip(m Meta, externalSubnet, address string) *unstructured.Unstructured {
	spec := map[string]any{"externalSubnet": externalSubnet, "type": "nat"}
	set(spec, "v4Ip", address)
	return object(OvnEipGVK, m, spec)
}

func OvnFip(m Meta, eip, ipName string) *unstructured.Unstructured {
	return object(OvnFipGVK, m, map[string]any{"ovnEip": eip, "ipName": ipName})
}

func RouterLBRule(m Meta, eip, vpc, namespace string, endpoints []string, ports []RouterLBPort, healthCheck bool) *unstructured.Unstructured {
	rendered := make([]any, len(ports))
	for i, p := range ports {
		rendered[i] = map[string]any{"name": p.Name, "port": int64(p.Port), "targetPort": int64(p.TargetPort), "protocol": p.Protocol}
	}
	annotated := m
	annotated.Annotations = maps.Clone(m.Annotations)
	if !healthCheck {
		if annotated.Annotations == nil {
			annotated.Annotations = map[string]string{}
		}
		annotated.Annotations[AnnotationServiceHealthCheck] = "false"
	}
	spec := map[string]any{"ovnEip": eip, "vpc": vpc, "namespace": namespace, "endpoints": anys(endpoints), "ports": rendered}
	return object(RouterLBRuleGVK, annotated, spec)
}

func DNSZone(m Meta, vpc string, records []DNSRecord) *unstructured.Unstructured {
	rendered := make([]any, len(records))
	for i, r := range records {
		rendered[i] = map[string]any{"name": r.Name, "ips": anys(r.IPs)}
	}
	return object(DNSZoneGVK, m, map[string]any{"vpc": vpc, "records": rendered})
}

func sgRules(rules []SGRule) []any {
	out := make([]any, len(rules))
	for i, r := range rules {
		rule := map[string]any{"ipVersion": r.IPVersion, "protocol": r.Protocol, "priority": int64(r.Priority), "remoteType": r.RemoteType, "policy": r.Policy}
		set(rule, "remoteAddress", r.RemoteAddress)
		set(rule, "remoteSecurityGroup", r.RemoteSecurityGroup)
		if r.PortMin > 0 || r.PortMax > 0 {
			rule["portRangeMin"] = int64(r.PortMin)
			rule["portRangeMax"] = int64(r.PortMax)
		}
		out[i] = rule
	}
	return out
}

func SecurityGroup(m Meta, ingress, egress []SGRule) *unstructured.Unstructured {
	spec := map[string]any{"allowSameGroupTraffic": false}
	if len(ingress) > 0 {
		spec["ingressRules"] = sgRules(ingress)
	}
	if len(egress) > 0 {
		spec["egressRules"] = sgRules(egress)
	}
	return object(SecurityGroupGVK, m, spec)
}

func ProviderNetwork(m Meta, iface string, nodeSelector map[string]string) *unstructured.Unstructured {
	return object(ProviderNetworkGVK, m, map[string]any{"defaultInterface": iface, "nodeSelector": selector(nodeSelector)})
}

func Vlan(m Meta, id int, provider string) *unstructured.Unstructured {
	return object(VlanGVK, m, map[string]any{"id": int64(id), "provider": provider})
}

func BgpConf(m Meta, localASN uint32, routerID string, peers []BgpPeer, nodeSelector map[string]string) *unstructured.Unstructured {
	rendered := make([]any, len(peers))
	neighbours := make([]any, len(peers))
	for i, p := range peers {
		rendered[i] = map[string]any{"address": p.Address, "asn": int64(p.ASN)}
		neighbours[i] = p.Address
	}
	spec := map[string]any{"localASN": int64(localASN), "peers": rendered, "neighbours": neighbours, "nodeSelector": selector(nodeSelector)}
	set(spec, "routerId", routerID)
	if len(peers) > 0 {
		spec["peerASN"] = int64(peers[0].ASN)
	}
	return object(BgpConfGVK, m, spec)
}
