package network

import (
	"context"
	"errors"
	"net/netip"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

var ErrNoFreeAddress = errors.New("the subnet has no free address")

func LowestFreeAddress(subnet v1alpha1.Subnet, taken map[netip.Addr]bool) (string, error) {
	prefix, err := netip.ParsePrefix(subnet.Spec.CIDR)
	if err != nil {
		return "", err
	}
	for addr := prefix.Masked().Addr().Next(); addr.IsValid() && prefix.Contains(addr); addr = addr.Next() {
		if !taken[addr] && CheckNICAddress(subnet, addr.String()) == nil {
			return addr.String(), nil
		}
	}
	return "", ErrNoFreeAddress
}

func TakenAddresses(ctx context.Context, live client.Reader, subnet v1alpha1.Subnet, reserved []string) (map[netip.Addr]bool, error) {
	taken := map[netip.Addr]bool{}
	add := func(address string) {
		if addr, err := netip.ParseAddr(address); err == nil {
			taken[addr] = true
		}
	}
	for _, address := range reserved {
		add(address)
	}
	ips, err := listObjects(ctx, live, fabric.IPGVK, client.MatchingLabels{labelFabricSubnet: FabricName(subnet.Namespace, subnet.Name)})
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		add(fabric.IPAddress(ip))
	}
	var machines v1alpha1.VirtualMachineList
	if err := live.List(ctx, &machines, client.InNamespace(subnet.Namespace)); err != nil {
		return nil, err
	}
	for _, machine := range machines.Items {
		for _, nic := range machine.Spec.Networks {
			if nic.SubnetRef == subnet.Name {
				add(nic.Address)
			}
		}
	}
	var containers v1alpha1.ContainerList
	if err := live.List(ctx, &containers, client.InNamespace(subnet.Namespace)); err != nil {
		return nil, err
	}
	for _, container := range containers.Items {
		if subnetOfContainer(container) == subnet.Name {
			add(container.Spec.Network.Address)
		}
	}
	return taken, nil
}

func subnetOfContainer(container v1alpha1.Container) string {
	if container.Spec.Network.SubnetRef == "" {
		return defaultSubnetName
	}
	return container.Spec.Network.SubnetRef
}

func AllocateAddress(ctx context.Context, live client.Reader, subnet v1alpha1.Subnet, reserved []string) (string, error) {
	taken, err := TakenAddresses(ctx, live, subnet, reserved)
	if err != nil {
		return "", err
	}
	return LowestFreeAddress(subnet, taken)
}
