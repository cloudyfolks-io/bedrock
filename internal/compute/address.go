package compute

import (
	"cmp"
	"context"
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func olderVM(a, b v1alpha1.VirtualMachine) bool {
	return cmp.Or(a.CreationTimestamp.Time.Compare(b.CreationTimestamp.Time), cmp.Compare(a.Name, b.Name)) < 0
}

func pinnedBy(vms []v1alpha1.VirtualMachine, vm v1alpha1.VirtualMachine, subnet, address string) (string, bool) {
	for _, other := range vms {
		pins := slices.ContainsFunc(other.Spec.Networks, func(n v1alpha1.VMNetwork) bool {
			return n.SubnetRef == subnet && n.Address == address
		})
		if pins && other.Name != vm.Name && olderVM(other, vm) {
			return other.Name, true
		}
	}
	return "", false
}

func holdsAddress(vm v1alpha1.VirtualMachine, nic NIC) bool {
	return slices.ContainsFunc(vm.Status.Addresses, func(a v1alpha1.VMAddress) bool {
		return a.Subnet == nic.Subnet && a.IP == nic.Address
	})
}

func awaitsPinnedAddress(vm v1alpha1.VirtualMachine, obs observation) bool {
	return obs.Phase == v1alpha1.VMPhaseStarting && slices.ContainsFunc(vm.Spec.Networks, func(n v1alpha1.VMNetwork) bool {
		return n.Address != "" && !slices.ContainsFunc(obs.Addresses, func(a v1alpha1.VMAddress) bool {
			return a.Subnet == n.SubnetRef && a.IP == n.Address
		})
	})
}

func (r *VirtualMachineReconciler) stepAddresses(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	for _, nic := range in.NICs {
		if nic.Address == "" {
			continue
		}
		if gate, err := r.addressGate(ctx, vm, nic); err != nil || gate != nil {
			return in, gate, err
		}
	}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) addressGate(ctx context.Context, vm v1alpha1.VirtualMachine, nic NIC) (*Gate, error) {
	var subnet v1alpha1.Subnet
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: vm.Namespace, Name: nic.Subnet}, &subnet); err != nil {
		return nil, err
	}
	if violation := network.CheckNICAddress(subnet, nic.Address); violation != nil {
		return &Gate{Reason: violation.Reason, Message: violation.Message}, nil
	}
	if holdsAddress(vm, nic) {
		return nil, nil
	}
	var vms v1alpha1.VirtualMachineList
	if err := r.Client.List(ctx, &vms, client.InNamespace(vm.Namespace)); err != nil {
		return nil, err
	}
	if other, held := pinnedBy(vms.Items, vm, nic.Subnet, nic.Address); held {
		return gateOf(ReasonAddressInUse, "the address %s of the subnet %q is pinned by the virtual machine %q", nic.Address, nic.Subnet, other), nil
	}
	ips, err := network.IPsOfAddress(ctx, r.FabricIPs, network.FabricName(vm.Namespace, nic.Subnet), nic.Address)
	if err != nil {
		return nil, err
	}
	if network.AddressHeldByAnother(ips, nic.Address, vm.Namespace, vm.Name) {
		return gateOf(ReasonAddressInUse, "the address %s of the subnet %q is already allocated to another pod", nic.Address, nic.Subnet), nil
	}
	return nil, nil
}
