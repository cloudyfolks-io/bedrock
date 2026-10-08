package network

import (
	"context"
	"slices"
	"strconv"
	"strings"

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
)

type Peer = settings.BGPPeer

type BGP struct {
	LocalASN uint32
	RouterID string
	Peers    []Peer
	Transit  string
}

type Config struct {
	EIPMode          string
	ProviderNetworks []v1alpha1.ProviderNetworkSpec
	BGP              BGP
	DNSServers       []string
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
	peers, err := settings.ParseBGPPeers(effective[keyBGPPeers])
	if err != nil {
		return Config{}, err
	}
	asn, _ := strconv.ParseUint(effective[keyBGPLocalASN], 10, 32)
	return Config{
		EIPMode:          effective[keyEIPMode],
		ProviderNetworks: networks,
		BGP:              BGP{LocalASN: uint32(asn), RouterID: effective[keyBGPRouterID], Peers: peers, Transit: effective[keyBGPTransit]},
		DNSServers:       splitList(effective[keyDNSServers]),
	}, nil
}

var networkKeys = []string{keyEIPMode, keyProviderNetwork, keyBGPLocalASN, keyBGPRouterID, keyBGPPeers, keyBGPTransit, keyDNSServers}

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

func (c Config) Provider(name string) (v1alpha1.ProviderNetworkSpec, bool) {
	index := slices.IndexFunc(c.ProviderNetworks, func(pn v1alpha1.ProviderNetworkSpec) bool { return pn.Name == name })
	if index < 0 {
		return v1alpha1.ProviderNetworkSpec{}, false
	}
	return c.ProviderNetworks[index], true
}

func (c Config) MissingBGPSettings() []string {
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
	if _, found := c.Provider(c.BGP.Transit); !found {
		missing = append(missing, keyBGPTransit+"="+c.BGP.Transit+" (not a provider network)")
	}
	return missing
}
