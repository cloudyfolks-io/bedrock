package network

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const bgpConfName = "bedrock"

func underlayMeta(kind, name string) fabric.Meta {
	labels := ObjectLabels(kind, name, "", "")
	labels[LabelComponent] = ComponentUnderlay
	return fabric.Meta{Name: name, Labels: labels}
}

func gatewayNodes() map[string]string {
	return map[string]string{fabric.LabelExternalGateway: "true"}
}

func providerObjects(cfg Config, pn v1alpha1.ProviderNetworkSpec) []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		fabric.ProviderNetwork(underlayMeta("ProviderNetwork", pn.Name), pn.Interface, gatewayNodes()),
		fabric.Vlan(underlayMeta("Vlan", pn.Name), int(pn.Vlan), pn.Name),
		externalVlanSubnet(cfg, pn),
	}
}

func poolMeta(pn v1alpha1.ProviderNetworkSpec) fabric.Meta {
	meta := underlayMeta("Subnet", ExternalSubnetName(pn.Name))
	meta.Labels[LabelUsability] = Usability(pn.Role)
	return meta
}

func externalVlanSubnet(cfg Config, pn v1alpha1.ProviderNetworkSpec) *unstructured.Unstructured {
	return fabric.Subnet(poolMeta(pn), fabric.SubnetSpec{CIDR: pn.CIDR, Gateway: pn.Gateway, Vlan: pn.Name, ExcludeIPs: pn.ExcludeIPs, ACLs: servicesACLs(cfg, pn)})
}

func externalOverlaySubnet(pn v1alpha1.ProviderNetworkSpec) *unstructured.Unstructured {
	return fabric.Subnet(poolMeta(pn), fabric.SubnetSpec{CIDR: pn.CIDR, Gateway: pn.Gateway, ExcludeIPs: pn.ExcludeIPs})
}

func bgpConf(cfg Config) *unstructured.Unstructured {
	peers := make([]fabric.BgpPeer, len(cfg.BGP.Peers))
	for i, p := range cfg.BGP.Peers {
		peers[i] = fabric.BgpPeer{Address: p.Address, ASN: p.ASN, BFD: cfg.BGP.BFD}
	}
	return fabric.BgpConf(underlayMeta("BgpConf", bgpConfName), fabric.BgpConfSpec{
		LocalASN: cfg.BGP.LocalASN, RouterID: cfg.BGP.RouterID, Peers: peers, NodeSelector: gatewayNodes(),
		HoldTime: cfg.BGP.HoldTime, KeepaliveTime: cfg.BGP.KeepaliveTime, GracefulRestart: cfg.BGP.GracefulRestart,
		AdvertiseFilter: FilterWithout(cfg.BGP.AdvertiseFilter, cfg.LoadBalancer.CIDR), RedistributeTables: cfg.BGP.RedistributeTables,
	})
}

func RenderUnderlay(cfg Config) ([]*unstructured.Unstructured, []string) {
	if cfg.EIPMode != EIPModeBGP {
		return renderL2(cfg), nil
	}
	return renderBGP(cfg)
}

func renderL2(cfg Config) []*unstructured.Unstructured {
	var objects []*unstructured.Unstructured
	for _, pn := range cfg.ProviderNetworks {
		objects = append(objects, providerObjects(cfg, pn)...)
	}
	return objects
}

func renderBGP(cfg Config) ([]*unstructured.Unstructured, []string) {
	var objects []*unstructured.Unstructured
	for _, pn := range cfg.ProviderNetworks {
		switch {
		case pn.Name == cfg.BGP.Transit:
			pn.Role = v1alpha1.ProviderRoleTransit
			objects = append(objects, providerObjects(cfg, pn)...)
		case isServicesPool(pn):
			objects = append(objects, providerObjects(cfg, pn)...)
		default:
			objects = append(objects, externalOverlaySubnet(pn))
		}
	}
	if missing := MissingBGPSettings(cfg); len(missing) > 0 {
		return objects, []string{fmt.Sprintf("BGP mode needs these settings before the BgpConf is rendered: %s", strings.Join(missing, ", "))}
	}
	return append(objects, bgpConf(cfg)), nil
}
