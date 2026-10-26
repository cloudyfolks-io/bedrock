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

type vlanKey struct {
	iface string
	id    int32
}

func providerObjects(cfg Config, pns []v1alpha1.ProviderNetworkSpec) []*unstructured.Unstructured {
	var objects []*unstructured.Unstructured
	carriers := map[string]string{}
	vlans := map[vlanKey]string{}
	for _, pn := range pns {
		carrier, known := carriers[pn.Interface]
		if !known {
			carrier = pn.Name
			carriers[pn.Interface] = carrier
			objects = append(objects, fabric.ProviderNetwork(underlayMeta("ProviderNetwork", carrier), pn.Interface, gatewayNodes()))
		}
		key := vlanKey{pn.Interface, pn.Vlan}
		vlan, known := vlans[key]
		if !known {
			vlan = pn.Name
			vlans[key] = vlan
			objects = append(objects, fabric.Vlan(underlayMeta("Vlan", vlan), int(pn.Vlan), carrier))
		}
		objects = append(objects, externalVlanSubnet(cfg, pn, vlan))
	}
	return objects
}

func poolMeta(pn v1alpha1.ProviderNetworkSpec) fabric.Meta {
	meta := underlayMeta("Subnet", ExternalSubnetName(pn.Name))
	meta.Labels[LabelUsability] = Usability(pn.Role)
	return meta
}

func externalVlanSubnet(cfg Config, pn v1alpha1.ProviderNetworkSpec, vlan string) *unstructured.Unstructured {
	return fabric.Subnet(poolMeta(pn), fabric.SubnetSpec{CIDR: pn.CIDR, Gateway: pn.Gateway, Vlan: vlan, ExcludeIPs: pn.ExcludeIPs, ACLs: servicesACLs(cfg, pn)})
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
	return providerObjects(cfg, cfg.ProviderNetworks)
}

func renderBGP(cfg Config) ([]*unstructured.Unstructured, []string) {
	var underlay []v1alpha1.ProviderNetworkSpec
	var overlay []*unstructured.Unstructured
	for _, pn := range cfg.ProviderNetworks {
		switch {
		case pn.Name == cfg.BGP.Transit:
			pn.Role = v1alpha1.ProviderRoleTransit
			underlay = append(underlay, pn)
		case isServicesPool(pn):
			underlay = append(underlay, pn)
		default:
			overlay = append(overlay, externalOverlaySubnet(pn))
		}
	}
	objects := append(providerObjects(cfg, underlay), overlay...)
	if missing := MissingBGPSettings(cfg); len(missing) > 0 {
		return objects, []string{fmt.Sprintf("BGP mode needs these settings before the BgpConf is rendered: %s", strings.Join(missing, ", "))}
	}
	return append(objects, bgpConf(cfg)), nil
}
