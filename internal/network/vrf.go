package network

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func VrfReserved(id uint32, redistributedTables []uint32) *Violation {
	if !slices.Contains(redistributedTables, id) {
		return nil
	}
	return &Violation{Reason: ReasonVRFReserved, Message: fmt.Sprintf("the VRF id %d is a redistributed host table; remove it from network.bgp.redistribute-tables or pin another id", id)}
}

func HeldVrfID(network v1alpha1.Network) (uint32, bool) {
	if network.Spec.VrfID != nil {
		return uint32(*network.Spec.VrfID), true
	}
	id, err := strconv.ParseUint(network.Annotations[AnnotationVrfID], 10, 32)
	return uint32(id), err == nil
}

func VrfInUse(network v1alpha1.Network, networks []v1alpha1.Network, foreignVpcIDs []uint32) *Violation {
	id := uint32(*network.Spec.VrfID)
	for _, other := range networks {
		held, ok := HeldVrfID(other)
		if other.Name == network.Name || !ok || held != id {
			continue
		}
		if other.Spec.VrfID != nil && !Older(&other, &network) {
			continue
		}
		return &Violation{Reason: ReasonVRFInUse, Message: fmt.Sprintf("the Network %s holds the VRF id %d; pin another id", other.Name, id)}
	}
	if slices.Contains(foreignVpcIDs, id) {
		return &Violation{Reason: ReasonVRFInUse, Message: fmt.Sprintf("a Fabric Vpc that this Network does not own holds the VRF id %d; pin another id", id)}
	}
	return nil
}

func Refusal(rangeBreach, vrfBreach *Violation, provisioned bool) *Violation {
	switch {
	case rangeBreach != nil && !provisioned:
		return rangeBreach
	case vrfBreach != nil && (!provisioned || vrfBreach.Reason == ReasonVRFInUse):
		return vrfBreach
	}
	return nil
}
