package network

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const connectPrefixBits = 30

type PeerLink struct {
	RemoteVpc    string
	LocalIP      string
	RemoteIP     string
	RemoteRanges []string
}

func activePeerings(peerings []v1alpha1.NetworkPeering) []v1alpha1.NetworkPeering {
	return slices.DeleteFunc(slices.Clone(peerings), func(p v1alpha1.NetworkPeering) bool { return !p.DeletionTimestamp.IsZero() })
}

func samePair(a, b v1alpha1.NetworkPeering) bool {
	x, y := slices.Sorted(slices.Values(a.Spec.NetworkRefs)), slices.Sorted(slices.Values(b.Spec.NetworkRefs))
	return slices.Equal(x, y)
}

func holdsConnectCIDR(holder, claimant v1alpha1.NetworkPeering, cidr string) bool {
	return holder.Status.ConnectCIDR == cidr || (holder.Spec.ConnectCIDR == cidr && Older(&holder, &claimant))
}

func ResolvePeering(self v1alpha1.NetworkPeering, all []v1alpha1.NetworkPeering, rangeCIDR string) (string, *Violation) {
	others := slices.DeleteFunc(activePeerings(all), func(p v1alpha1.NetworkPeering) bool { return p.Name == self.Name })
	if i := slices.IndexFunc(others, func(p v1alpha1.NetworkPeering) bool { return samePair(p, self) && Older(&p, &self) }); i >= 0 {
		return "", &Violation{Reason: ReasonPeeringExists, Message: fmt.Sprintf("the NetworkPeering %s already peers these Networks", others[i].Name)}
	}
	if explicit := self.Spec.ConnectCIDR; explicit != "" {
		if i := slices.IndexFunc(others, func(p v1alpha1.NetworkPeering) bool { return holdsConnectCIDR(p, self, explicit) }); i >= 0 {
			return "", &Violation{Reason: ReasonConnectCIDRInUse, Message: fmt.Sprintf("the NetworkPeering %s holds the connect network %s; use another connectCIDR", others[i].Name, explicit)}
		}
		return explicit, nil
	}
	if recorded := self.Status.ConnectCIDR; recorded != "" && !slices.ContainsFunc(others, func(p v1alpha1.NetworkPeering) bool { return holdsConnectCIDR(p, self, recorded) }) {
		return recorded, nil
	}
	return allocateConnectCIDR(rangeCIDR, others)
}

func allocateConnectCIDR(rangeCIDR string, others []v1alpha1.NetworkPeering) (string, *Violation) {
	prefix, err := netip.ParsePrefix(rangeCIDR)
	if err != nil {
		return "", &Violation{Reason: ReasonConnectCIDRInUse, Message: fmt.Sprintf("network.peering.range %q is not a network", rangeCIDR)}
	}
	taken := map[string]bool{}
	for _, p := range others {
		taken[p.Spec.ConnectCIDR] = true
		taken[p.Status.ConnectCIDR] = true
	}
	for slot := netip.PrefixFrom(prefix.Addr(), connectPrefixBits); prefix.Contains(slot.Addr()); slot = nextSlot(slot) {
		if !taken[slot.String()] {
			return slot.String(), nil
		}
	}
	return "", &Violation{Reason: ReasonConnectCIDRInUse, Message: fmt.Sprintf("every /30 of network.peering.range %s is in use", rangeCIDR)}
}

func nextSlot(slot netip.Prefix) netip.Prefix {
	return netip.PrefixFrom(hostOf(slot, 4), connectPrefixBits)
}

func hostOf(base netip.Prefix, offset int) netip.Addr {
	addr := base.Addr()
	for range offset {
		addr = addr.Next()
	}
	return addr
}

func PeerLinks(network string, peerings []v1alpha1.NetworkPeering, networks []v1alpha1.Network) []PeerLink {
	var links []PeerLink
	for _, p := range activePeerings(peerings) {
		link, ok := peerLink(network, p, networks)
		if ok {
			links = append(links, link)
		}
	}
	slices.SortFunc(links, func(a, b PeerLink) int { return cmp.Compare(a.RemoteVpc, b.RemoteVpc) })
	return links
}

func peerLink(network string, p v1alpha1.NetworkPeering, networks []v1alpha1.Network) (PeerLink, bool) {
	connect, err := netip.ParsePrefix(p.Status.ConnectCIDR)
	if err != nil || len(p.Spec.NetworkRefs) != 2 {
		return PeerLink{}, false
	}
	side := slices.Index(p.Spec.NetworkRefs, network)
	if side < 0 {
		return PeerLink{}, false
	}
	remote := p.Spec.NetworkRefs[1-side]
	i := slices.IndexFunc(networks, func(n v1alpha1.Network) bool { return n.Name == remote && n.DeletionTimestamp.IsZero() })
	if i < 0 {
		return PeerLink{}, false
	}
	local := netip.PrefixFrom(hostOf(connect, side+1), connectPrefixBits)
	return PeerLink{RemoteVpc: VpcName(remote), LocalIP: local.String(), RemoteIP: hostOf(connect, 2-side).String(), RemoteRanges: networks[i].Spec.Ranges()}, true
}

func NetworksOfPeering(p v1alpha1.NetworkPeering) []reconcile.Request {
	requests := make([]reconcile.Request, len(p.Spec.NetworkRefs))
	for i, ref := range p.Spec.NetworkRefs {
		requests[i] = requestOf("", ref)
	}
	return requests
}

func PeeringsOfNetwork(peerings []v1alpha1.NetworkPeering, network string) []reconcile.Request {
	var requests []reconcile.Request
	for _, p := range peerings {
		if slices.Contains(p.Spec.NetworkRefs, network) {
			requests = append(requests, requestOf("", p.Name))
		}
	}
	return requests
}

func PeersOfNetwork(peerings []v1alpha1.NetworkPeering, network string) []reconcile.Request {
	var requests []reconcile.Request
	for _, p := range peerings {
		if i := slices.Index(p.Spec.NetworkRefs, network); i >= 0 && len(p.Spec.NetworkRefs) == 2 {
			requests = append(requests, requestOf("", p.Spec.NetworkRefs[1-i]))
		}
	}
	return requests
}
