package network

import (
	"fmt"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const geneveOverhead = 100

func UnderlayMTU(hosts []v1alpha1.Host) int {
	underlay := 0
	for _, host := range hosts {
		for _, nic := range host.Status.Inventory.NICs {
			if nic.Link && nic.MTU > 0 && (underlay == 0 || int(nic.MTU) < underlay) {
				underlay = int(nic.MTU)
			}
		}
	}
	return underlay
}

func MTUViolation(mtu, underlay int) *Violation {
	if mtu <= 0 || underlay <= 0 || mtu <= underlay-geneveOverhead {
		return nil
	}
	return &Violation{Reason: ReasonMTUExceedsUnderlay, Message: fmt.Sprintf("the MTU %d is more than %d, the underlay MTU %d minus %d bytes of Geneve overhead", mtu, underlay-geneveOverhead, underlay, geneveOverhead)}
}
