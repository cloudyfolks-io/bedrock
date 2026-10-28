package network

import (
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const geneveOverhead = 100

func tunnelMTU(node corev1.Node, hosts []v1alpha1.Host) int {
	index := slices.IndexFunc(hosts, func(h v1alpha1.Host) bool { return h.Name == node.Name })
	if index < 0 {
		return 0
	}
	for _, address := range node.Status.Addresses {
		if address.Type != corev1.NodeInternalIP {
			continue
		}
		for _, link := range hosts[index].Status.Inventory.Links {
			if slices.Contains(link.Addresses, address.Address) {
				return int(link.MTU)
			}
		}
	}
	return 0
}

func UnderlayMTU(nodes []corev1.Node, hosts []v1alpha1.Host) int {
	underlay := 0
	for _, node := range nodes {
		mtu := tunnelMTU(node, hosts)
		if mtu <= 0 {
			return 0
		}
		if underlay == 0 || mtu < underlay {
			underlay = mtu
		}
	}
	return underlay
}

func MTUViolation(mtu, underlay int) *Violation {
	if mtu <= 0 || underlay <= 0 || mtu <= underlay-geneveOverhead {
		return nil
	}
	return &Violation{Reason: ReasonMTUExceedsUnderlay, Message: fmt.Sprintf("the MTU %d is more than %d, the MTU %d of the tunnel interface minus %d bytes of Geneve overhead", mtu, underlay-geneveOverhead, underlay, geneveOverhead)}
}
