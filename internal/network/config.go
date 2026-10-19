package network

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	EIPModeL2  = "l2"
	EIPModeBGP = "bgp"

	keyEIPMode         = "network.eip-mode"
	keyProviderNetwork = "network.provider-networks"
	keyBGPLocalASN     = "network.bgp.local-asn"
	keyBGPRouterID     = "network.bgp.router-id"
	keyBGPPeers        = "network.bgp.peers"
	keyBGPTransit      = "network.bgp.transit-network"
	keyDNSServers      = "network.dns-servers"
	keyMTU             = "network.mtu"

	keyLBCIDR    = "network.load-balancer.cidr"
	keyLBGateway = "network.load-balancer.gateway"

	keyReservedCIDRs       = "network.reserved-cidrs"
	keySubnetAllowedRanges = "network.subnet-allowed-ranges"

	keyBGPBFD                = "network.bgp.bfd"
	keyBGPHoldTime           = "network.bgp.hold-time"
	keyBGPKeepaliveTime      = "network.bgp.keepalive-time"
	keyBGPGracefulRestart    = "network.bgp.graceful-restart"
	keyBGPAdvertiseFilter    = "network.bgp.advertise-filter"
	keyBGPRedistributeTables = "network.bgp.redistribute-tables"
)

type Peer = settings.BGPPeer

type BGP struct {
	LocalASN           uint32
	RouterID           string
	Peers              []Peer
	Transit            string
	BFD                bool
	HoldTime           time.Duration
	KeepaliveTime      time.Duration
	GracefulRestart    bool
	AdvertiseFilter    []string
	RedistributeTables []uint32
}

type LoadBalancerPlane struct {
	CIDR    string
	Gateway string
}

type Config struct {
	EIPMode          string
	ProviderNetworks []v1alpha1.ProviderNetworkSpec
	BGP              BGP
	DNSServers       []string
	MTU              int
	LoadBalancer     LoadBalancerPlane

	ReservedCIDRs       []string
	SubnetAllowedRanges []string
}

func ParseConfig(values map[string]string) (Config, error) {
	effective := make(map[string]string)
	for _, key := range networkKeys {
		def, _ := settings.Lookup(key)
		value := values[key]
		if value == "" {
			value = def.Default
		}
		if err := def.Validate(value); err != nil {
			return Config{}, err
		}
		effective[key] = value
	}
	networks, err := settings.DecodeProviderNetworks(effective[keyProviderNetwork])
	if err != nil {
		return Config{}, err
	}
	bgp, err := parseBGP(effective)
	if err != nil {
		return Config{}, err
	}
	mtu, _ := strconv.Atoi(effective[keyMTU])
	plane, err := parseLoadBalancerPlane(effective)
	if err != nil {
		return Config{}, err
	}
	return Config{
		EIPMode:          effective[keyEIPMode],
		ProviderNetworks: networks,
		BGP:              bgp,
		DNSServers:       splitList(effective[keyDNSServers]),
		MTU:              mtu,
		LoadBalancer:     plane,

		ReservedCIDRs:       splitList(effective[keyReservedCIDRs]),
		SubnetAllowedRanges: splitList(effective[keySubnetAllowedRanges]),
	}, nil
}

func parseLoadBalancerPlane(effective map[string]string) (LoadBalancerPlane, error) {
	prefix, _ := netip.ParsePrefix(effective[keyLBCIDR])
	gateway, _ := netip.ParseAddr(effective[keyLBGateway])
	if !prefix.Contains(gateway) || isNetworkOrBroadcast(prefix, gateway) {
		return LoadBalancerPlane{}, fmt.Errorf("the setting %s %q must be a host address inside %s %q", keyLBGateway, gateway, keyLBCIDR, prefix)
	}
	return LoadBalancerPlane{CIDR: prefix.String(), Gateway: gateway.String()}, nil
}

func parseBGP(effective map[string]string) (BGP, error) {
	peers, err := settings.ParseBGPPeers(effective[keyBGPPeers])
	if err != nil {
		return BGP{}, err
	}
	filter, err := settings.ParseAdvertiseFilter(effective[keyBGPAdvertiseFilter])
	if err != nil {
		return BGP{}, err
	}
	tables, err := settings.ParseTableIDs(effective[keyBGPRedistributeTables])
	if err != nil {
		return BGP{}, err
	}
	asn, _ := strconv.ParseUint(effective[keyBGPLocalASN], 10, 32)
	bfd, _ := strconv.ParseBool(effective[keyBGPBFD])
	graceful, _ := strconv.ParseBool(effective[keyBGPGracefulRestart])
	hold, _ := time.ParseDuration(effective[keyBGPHoldTime])
	keepalive, _ := time.ParseDuration(effective[keyBGPKeepaliveTime])
	return BGP{
		LocalASN: uint32(asn), RouterID: effective[keyBGPRouterID], Peers: peers, Transit: effective[keyBGPTransit],
		BFD: bfd, HoldTime: hold, KeepaliveTime: keepalive, GracefulRestart: graceful, AdvertiseFilter: filter, RedistributeTables: tables,
	}, nil
}

var networkKeys = []string{
	keyEIPMode, keyProviderNetwork, keyBGPLocalASN, keyBGPRouterID, keyBGPPeers, keyBGPTransit, keyDNSServers, keyMTU,
	keyBGPBFD, keyBGPHoldTime, keyBGPKeepaliveTime, keyBGPGracefulRestart, keyBGPAdvertiseFilter, keyBGPRedistributeTables,
	keyReservedCIDRs, keySubnetAllowedRanges, keyLBCIDR, keyLBGateway,
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func LoadConfig(ctx context.Context, c client.Reader) (Config, error) {
	var list v1alpha1.SettingList
	if err := c.List(ctx, &list); err != nil {
		return Config{}, err
	}
	values := make(map[string]string, len(list.Items))
	for _, setting := range list.Items {
		values[setting.Name] = setting.Spec.Value
	}
	return ParseConfig(values)
}

func ProviderOf(c Config, name string) (v1alpha1.ProviderNetworkSpec, bool) {
	index := slices.IndexFunc(c.ProviderNetworks, func(pn v1alpha1.ProviderNetworkSpec) bool { return pn.Name == name })
	if index < 0 {
		return v1alpha1.ProviderNetworkSpec{}, false
	}
	return c.ProviderNetworks[index], true
}

func MissingBGPSettings(c Config) []string {
	var missing []string
	if c.BGP.LocalASN == 0 {
		missing = append(missing, keyBGPLocalASN)
	}
	if len(c.BGP.Peers) == 0 {
		missing = append(missing, keyBGPPeers)
	}
	if c.BGP.Transit == "" {
		return append(missing, keyBGPTransit)
	}
	if _, found := ProviderOf(c, c.BGP.Transit); !found {
		missing = append(missing, keyBGPTransit+"="+c.BGP.Transit+" (not a provider network)")
	}
	return missing
}
