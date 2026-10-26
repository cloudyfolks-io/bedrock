package network

import (
	"fmt"
	"slices"
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

func interfaceOf(pn *unstructured.Unstructured) string {
	return nestedStringOf(pn, "spec", "defaultInterface")
}

func nestedStringOf(u *unstructured.Unstructured, path ...string) string {
	value, _, _ := unstructured.NestedString(u.Object, path...)
	return value
}

func providerReady(pn *unstructured.Unstructured) bool {
	nodes, _, _ := unstructured.NestedStringSlice(pn.Object, "status", "readyNodes")
	return len(nodes) > 0
}

func lowestName(pns []v1alpha1.ProviderNetworkSpec, keep func(v1alpha1.ProviderNetworkSpec) bool) string {
	names := []string{}
	for _, pn := range pns {
		if keep(pn) {
			names = append(names, pn.Name)
		}
	}
	return slices.Min(names)
}

func carrierFor(iface string, pns []v1alpha1.ProviderNetworkSpec, existing []*unstructured.Unstructured) string {
	owners := slices.DeleteFunc(slices.Clone(existing), func(o *unstructured.Unstructured) bool {
		return o.GetKind() != "ProviderNetwork" || interfaceOf(o) != iface
	})
	if len(owners) == 0 {
		return lowestName(pns, func(pn v1alpha1.ProviderNetworkSpec) bool { return pn.Interface == iface })
	}
	slices.SortFunc(owners, func(x, y *unstructured.Unstructured) int {
		if providerReady(x) != providerReady(y) {
			if providerReady(x) {
				return -1
			}
			return 1
		}
		return strings.Compare(x.GetName(), y.GetName())
	})
	return owners[0].GetName()
}

type vlanChoice struct {
	name     string
	provider string
}

func vlanFor(pn v1alpha1.ProviderNetworkSpec, carrier string, pns []v1alpha1.ProviderNetworkSpec, existing []*unstructured.Unstructured) vlanChoice {
	onInterface := map[string]bool{}
	for _, o := range existing {
		if o.GetKind() == "ProviderNetwork" && interfaceOf(o) == pn.Interface {
			onInterface[o.GetName()] = true
		}
	}
	matches := slices.DeleteFunc(slices.Clone(existing), func(o *unstructured.Unstructured) bool {
		id, _, _ := unstructured.NestedInt64(o.Object, "spec", "id")
		return o.GetKind() != "Vlan" || id != int64(pn.Vlan) || !onInterface[nestedStringOf(o, "spec", "provider")]
	})
	if len(matches) == 0 {
		name := lowestName(pns, func(other v1alpha1.ProviderNetworkSpec) bool {
			return other.Interface == pn.Interface && other.Vlan == pn.Vlan
		})
		return vlanChoice{name: name, provider: carrier}
	}
	slices.SortFunc(matches, func(x, y *unstructured.Unstructured) int {
		xc, yc := nestedStringOf(x, "spec", "provider") == carrier, nestedStringOf(y, "spec", "provider") == carrier
		if xc != yc {
			if xc {
				return -1
			}
			return 1
		}
		return strings.Compare(x.GetName(), y.GetName())
	})
	return vlanChoice{name: matches[0].GetName(), provider: nestedStringOf(matches[0], "spec", "provider")}
}

func providerObjects(cfg Config, pns []v1alpha1.ProviderNetworkSpec, existing []*unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	var objects []*unstructured.Unstructured
	carriers := map[string]string{}
	emitted := map[string]bool{}
	vlans := map[vlanKey]vlanChoice{}
	emitProvider := func(name, iface string) {
		if !emitted[name] {
			emitted[name] = true
			objects = append(objects, fabric.ProviderNetwork(underlayMeta("ProviderNetwork", name), iface, gatewayNodes()))
		}
	}
	for _, pn := range pns {
		carrier, known := carriers[pn.Interface]
		if !known {
			carrier = carrierFor(pn.Interface, pns, existing)
			carriers[pn.Interface] = carrier
			emitProvider(carrier, pn.Interface)
		}
		key := vlanKey{pn.Interface, pn.Vlan}
		vlan, known := vlans[key]
		if !known {
			vlan = vlanFor(pn, carrier, pns, existing)
			vlans[key] = vlan
			emitProvider(vlan.provider, pn.Interface)
			objects = append(objects, fabric.Vlan(underlayMeta("Vlan", vlan.name), int(pn.Vlan), vlan.provider))
		}
		subnet, err := externalVlanSubnet(cfg, pn, vlan.name)
		if err != nil {
			return nil, err
		}
		objects = append(objects, subnet)
	}
	return objects, nil
}

func poolMeta(pn v1alpha1.ProviderNetworkSpec) fabric.Meta {
	meta := underlayMeta("Subnet", ExternalSubnetName(pn.Name))
	meta.Labels[LabelUsability] = Usability(pn.Role)
	return meta
}

func externalVlanSubnet(cfg Config, pn v1alpha1.ProviderNetworkSpec, vlan string) (*unstructured.Unstructured, error) {
	acls, err := servicesACLs(cfg, pn)
	if err != nil {
		return nil, err
	}
	return fabric.Subnet(poolMeta(pn), fabric.SubnetSpec{CIDR: pn.CIDR, Gateway: pn.Gateway, Vlan: vlan, ExcludeIPs: pn.ExcludeIPs, ACLs: acls}), nil
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
	objects, warnings, err := RenderUnderlayOver(cfg, nil)
	if err != nil {
		return nil, []string{err.Error()}
	}
	return objects, warnings
}

func RenderUnderlayOver(cfg Config, existing []*unstructured.Unstructured) ([]*unstructured.Unstructured, []string, error) {
	if cfg.EIPMode != EIPModeBGP {
		objects, err := providerObjects(cfg, cfg.ProviderNetworks, existing)
		return objects, nil, err
	}
	return renderBGP(cfg, existing)
}

func renderBGP(cfg Config, existing []*unstructured.Unstructured) ([]*unstructured.Unstructured, []string, error) {
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
	rendered, err := providerObjects(cfg, underlay, existing)
	if err != nil {
		return nil, nil, err
	}
	objects := append(rendered, overlay...)
	if missing := MissingBGPSettings(cfg); len(missing) > 0 {
		return objects, []string{fmt.Sprintf("BGP mode needs these settings before the BgpConf is rendered: %s", strings.Join(missing, ", "))}, nil
	}
	return append(objects, bgpConf(cfg)), nil, nil
}
