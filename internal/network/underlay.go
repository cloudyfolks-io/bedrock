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

func providerObjects(pn v1alpha1.ProviderNetworkSpec) []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		fabric.ProviderNetwork(underlayMeta("ProviderNetwork", pn.Name), pn.Interface, gatewayNodes()),
		fabric.Vlan(underlayMeta("Vlan", pn.Name), int(pn.Vlan), pn.Name),
		externalSubnet(pn, false),
	}
}

func externalSubnet(pn v1alpha1.ProviderNetworkSpec, overlay bool) *unstructured.Unstructured {
	name := ExternalSubnetName(pn.Name)
	return fabric.Subnet(underlayMeta("Subnet", name), fabric.SubnetSpec{CIDR: pn.CIDR, Gateway: pn.Gateway, Vlan: pn.Name, ExcludeIPs: pn.ExcludeIPs, Overlay: overlay})
}

func bgpConf(cfg Config) *unstructured.Unstructured {
	peers := make([]fabric.BgpPeer, len(cfg.BGP.Peers))
	for i, p := range cfg.BGP.Peers {
		peers[i] = fabric.BgpPeer{Address: p.Address, ASN: p.ASN}
	}
	return fabric.BgpConf(underlayMeta("BgpConf", bgpConfName), cfg.BGP.LocalASN, cfg.BGP.RouterID, peers, gatewayNodes())
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
		objects = append(objects, providerObjects(pn)...)
	}
	return objects
}

func renderBGP(cfg Config) ([]*unstructured.Unstructured, []string) {
	var objects []*unstructured.Unstructured
	for _, pn := range cfg.ProviderNetworks {
		if pn.Name == cfg.BGP.Transit {
			objects = append(objects, providerObjects(pn)...)
			continue
		}
		objects = append(objects, externalSubnet(pn, true))
	}
	if missing := cfg.MissingBGPSettings(); len(missing) > 0 {
		return objects, []string{fmt.Sprintf("BGP mode needs these settings before the BgpConf is rendered: %s", strings.Join(missing, ", "))}
	}
	return append(objects, bgpConf(cfg)), nil
}
