package fabric

import (
	"maps"
	"time"

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
	LocalOnly      bool
}

type Peering struct{ RemoteVpc, LocalConnectIP string }

type VpcSpec struct {
	EnableExternal       bool
	EnableBfd            bool
	ExtraExternalSubnets []string
	DefaultRoute         *StaticRoute
	StaticRoutes         []StaticRoute
	Peerings             []Peering
	Dynamic              *DynamicRouting
}

type SubnetSpec struct {
	Vpc, CIDR, Gateway, Provider, Vlan, DHCPOptions string
	ExcludeIPs                                      []string
	DHCP                                            bool
	MTU                                             int
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
	BFD     bool
}

type BgpConfSpec struct {
	LocalASN           uint32
	RouterID           string
	Peers              []BgpPeer
	NodeSelector       map[string]string
	HoldTime           time.Duration
	KeepaliveTime      time.Duration
	GracefulRestart    bool
	AdvertiseFilter    []string
	RedistributeTables []uint32
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

func stringMap(values map[string]string) map[string]any {
	out := make(map[string]any, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

func selector(labels map[string]string) map[string]any {
	return map[string]any{"matchLabels": stringMap(labels)}
}

func Vpc(m Meta, s VpcSpec) *unstructured.Unstructured {
	spec := map[string]any{}
	if s.EnableExternal {
		spec["enableExternal"] = true
		spec["extraExternalSubnets"] = anys(s.ExtraExternalSubnets)
	}
	if routes := vpcRoutes(s); len(routes) > 0 {
		spec["staticRoutes"] = routes
	}
	if len(s.Peerings) > 0 {
		spec["vpcPeerings"] = vpcPeerings(s.Peerings)
	}
	if s.EnableBfd {
		spec["enableBfd"] = true
	}
	if s.Dynamic != nil {
		spec["dynamicRouting"] = dynamicRouting(*s.Dynamic)
	}
	return object(VpcGVK, m, spec)
}

func vpcRoutes(s VpcSpec) []any {
	var routes []StaticRoute
	if s.DefaultRoute != nil {
		routes = append(routes, *s.DefaultRoute)
	}
	routes = append(routes, s.StaticRoutes...)
	out := make([]any, len(routes))
	for i, r := range routes {
		out[i] = map[string]any{"policy": "policyDst", "cidr": r.CIDR, "nextHopIP": r.NextHop}
	}
	return out
}

func vpcPeerings(peerings []Peering) []any {
	out := make([]any, len(peerings))
	for i, p := range peerings {
		out[i] = map[string]any{"remoteVpc": p.RemoteVpc, "localConnectIP": p.LocalConnectIP}
	}
	return out
}

func dynamicRouting(d DynamicRouting) map[string]any {
	routing := map[string]any{
		"enabled":        true,
		"redistribute":   anys(d.Redistribute),
		"vrfId":          int64(d.VrfID),
		"externalSubnet": d.ExternalSubnet,
	}
	if d.LocalOnly {
		routing["localOnly"] = true
	}
	return routing
}

func Subnet(m Meta, s SubnetSpec) *unstructured.Unstructured {
	spec := map[string]any{"protocol": "IPv4", "cidrBlock": s.CIDR, "gateway": s.Gateway}
	set(spec, "vpc", s.Vpc)
	set(spec, "dhcpV4Options", s.DHCPOptions)
	if s.MTU > 0 {
		spec["mtu"] = int64(s.MTU)
	}
	if len(s.ExcludeIPs) > 0 {
		spec["excludeIps"] = anys(s.ExcludeIPs)
	}
	if s.DHCP {
		spec["enableDHCP"] = true
	}
	set(spec, "vlan", s.Vlan)
	set(spec, "provider", s.Provider)
	if s.Provider != "" {
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

func routerLBRule(m Meta, eip, vpc, namespace string, endpoints []string, ports []RouterLBPort) *unstructured.Unstructured {
	rendered := make([]any, len(ports))
	for i, p := range ports {
		rendered[i] = map[string]any{"name": p.Name, "port": int64(p.Port), "targetPort": int64(p.TargetPort), "protocol": p.Protocol}
	}
	spec := map[string]any{"ovnEip": eip, "vpc": vpc, "namespace": namespace, "endpoints": anys(endpoints), "ports": rendered}
	return object(RouterLBRuleGVK, m, spec)
}

func RouterLBRule(m Meta, eip, vpc, namespace string, endpoints []string, ports []RouterLBPort) *unstructured.Unstructured {
	return routerLBRule(m, eip, vpc, namespace, endpoints, ports)
}

func RouterLBRuleWithoutHealthCheck(m Meta, eip, vpc, namespace string, endpoints []string, ports []RouterLBPort) *unstructured.Unstructured {
	silenced := m
	silenced.Annotations = maps.Clone(m.Annotations)
	if silenced.Annotations == nil {
		silenced.Annotations = map[string]string{}
	}
	silenced.Annotations[AnnotationServiceHealthCheck] = "false"
	return routerLBRule(silenced, eip, vpc, namespace, endpoints, ports)
}

func Vip(m Meta, namespace, subnet string) *unstructured.Unstructured {
	return object(VipGVK, m, map[string]any{"namespace": namespace, "subnet": subnet, "type": "switch_lb_vip"})
}

func SwitchLBRule(m Meta, vip, namespace string, endpoints []string, ports []RouterLBPort) *unstructured.Unstructured {
	rendered := make([]any, len(ports))
	for i, p := range ports {
		rendered[i] = map[string]any{"name": p.Name, "port": int64(p.Port), "targetPort": int64(p.TargetPort), "protocol": p.Protocol}
	}
	spec := map[string]any{"vip": vip, "namespace": namespace, "selector": anys(nil), "endpoints": anys(endpoints), "ports": rendered}
	return object(SwitchLBRuleGVK, m, spec)
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

func BgpConf(m Meta, s BgpConfSpec) *unstructured.Unstructured {
	rendered := make([]any, len(s.Peers))
	neighbours := make([]any, len(s.Peers))
	for i, p := range s.Peers {
		peer := map[string]any{"address": p.Address, "asn": int64(p.ASN)}
		if p.BFD {
			peer["bfd"] = true
		}
		rendered[i] = peer
		neighbours[i] = p.Address
	}
	spec := map[string]any{"localASN": int64(s.LocalASN), "peers": rendered, "neighbours": neighbours, "nodeSelector": stringMap(s.NodeSelector)}
	set(spec, "routerId", s.RouterID)
	set(spec, "holdTime", durationText(s.HoldTime))
	set(spec, "keepaliveTime", durationText(s.KeepaliveTime))
	if s.GracefulRestart {
		spec["gracefulRestart"] = true
	}
	if len(s.AdvertiseFilter) > 0 {
		spec["advertiseFilter"] = anys(s.AdvertiseFilter)
	}
	if len(s.RedistributeTables) > 0 {
		spec["redistributeTables"] = int64s(s.RedistributeTables)
	}
	if len(s.Peers) > 0 {
		spec["peerASN"] = int64(s.Peers[0].ASN)
	}
	return object(BgpConfGVK, m, spec)
}

func durationText(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

func int64s(values []uint32) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = int64(v)
	}
	return out
}
