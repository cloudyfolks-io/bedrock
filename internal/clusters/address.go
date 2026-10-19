package clusters

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	addressStartOffset     = 10
	fabricSubnetLabel      = "fabric.cloudyfolks.io/subnet"
	ReasonAddressExhausted = "AddressExhausted"
)

type AddressError struct {
	Reason  string
	Message string
}

func (e *AddressError) Error() string {
	return e.Message
}

func addressFailure(reason, format string, args ...any) *AddressError {
	return &AddressError{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

func NextFreeAddress(cidr netip.Prefix, gateway netip.Addr, excluded []string, taken []netip.Addr) (netip.Addr, error) {
	skipped, err := excludedRanges(excluded)
	if err != nil {
		return netip.Addr{}, err
	}
	network := cidr.Masked().Addr()
	last := lastHostAddress(cidr)
	candidate := network
	for range addressStartOffset {
		candidate = candidate.Next()
	}
	for ; candidate.IsValid() && candidate.Compare(last) <= 0; candidate = candidate.Next() {
		if candidate != gateway && !slices.Contains(taken, candidate) && !inRanges(skipped, candidate) {
			return candidate, nil
		}
	}
	return netip.Addr{}, addressFailure(ReasonAddressExhausted, "the subnet %s has no free address from offset %d up to its broadcast address", cidr, addressStartOffset)
}

type addressRange struct{ first, last netip.Addr }

func excludedRanges(entries []string) ([]addressRange, error) {
	ranges := make([]addressRange, 0, len(entries))
	for _, entry := range entries {
		first, last, err := settings.ParseAddressRange(entry)
		if err != nil {
			return nil, addressFailure(network.ReasonExcludeIPsInvalid, "the excludeIPs entry %q is not an address or an ascending range", entry)
		}
		ranges = append(ranges, addressRange{first: first, last: last})
	}
	return ranges, nil
}

func inRanges(ranges []addressRange, addr netip.Addr) bool {
	return slices.ContainsFunc(ranges, func(r addressRange) bool {
		return r.first.Compare(addr) <= 0 && addr.Compare(r.last) <= 0
	})
}

func lastHostAddress(cidr netip.Prefix) netip.Addr {
	bytes := cidr.Masked().Addr().AsSlice()
	for bit := cidr.Bits(); bit < len(bytes)*8; bit++ {
		bytes[bit/8] |= 1 << (7 - bit%8)
	}
	broadcast, _ := netip.AddrFromSlice(bytes)
	return broadcast.Prev()
}

func SubnetGateway(subnet v1alpha1.Subnet) (netip.Addr, error) {
	text := subnet.Spec.Gateway
	if text == "" {
		text, _ = network.DefaultGateway(subnet.Spec.CIDR)
	}
	return netip.ParseAddr(text)
}

type AddressReader struct {
	Clusters client.Reader
	IPs      client.Reader
}

func (r AddressReader) Taken(ctx context.Context, cluster v1alpha1.KubernetesCluster, subnetName string) ([]netip.Addr, error) {
	ips := &unstructured.UnstructuredList{}
	ips.SetGroupVersionKind(fabric.IPGVK.GroupVersion().WithKind("IPList"))
	if err := r.IPs.List(ctx, ips, client.MatchingLabels{fabricSubnetLabel: network.FabricName(cluster.Namespace, subnetName)}); err != nil {
		return nil, err
	}
	var clusters v1alpha1.KubernetesClusterList
	if err := r.Clusters.List(ctx, &clusters, client.InNamespace(cluster.Namespace)); err != nil {
		return nil, err
	}
	var vms v1alpha1.VirtualMachineList
	if err := r.Clusters.List(ctx, &vms, client.InNamespace(cluster.Namespace)); err != nil {
		return nil, err
	}
	var containers v1alpha1.ContainerList
	if err := r.Clusters.List(ctx, &containers, client.InNamespace(cluster.Namespace)); err != nil {
		return nil, err
	}
	taken := takenAddresses(ips.Items, clusters.Items, cluster)
	taken = append(taken, workerPins(clusters.Items, subnetName)...)
	taken = append(taken, machinePins(vms.Items, containers.Items, subnetName)...)
	return taken, nil
}

func takenAddresses(ips []unstructured.Unstructured, clusters []v1alpha1.KubernetesCluster, own v1alpha1.KubernetesCluster) []netip.Addr {
	var taken []netip.Addr
	for index := range ips {
		namespace, pod, _ := fabric.IPOwner(&ips[index])
		if namespace == own.Namespace && pod == ControlPlanePodName(own.Name) {
			continue
		}
		taken = appendAddress(taken, fabric.IPAddress(&ips[index]))
	}
	for _, other := range clusters {
		if other.Name == own.Name {
			continue
		}
		taken = appendAddress(taken, other.Status.ControlPlane.Address)
	}
	return taken
}

func workerPins(clusters []v1alpha1.KubernetesCluster, subnetName string) []netip.Addr {
	var pinned []netip.Addr
	for _, cluster := range clusters {
		if SubnetName(cluster.Spec) != subnetName {
			continue
		}
		for _, worker := range cluster.Spec.Workers {
			pinned = appendAddress(pinned, worker.Address)
		}
	}
	return pinned
}

func machinePins(vms []v1alpha1.VirtualMachine, containers []v1alpha1.Container, subnetName string) []netip.Addr {
	var pinned []netip.Addr
	for _, vm := range vms {
		for _, nic := range vm.Spec.Networks {
			if nic.SubnetRef == subnetName {
				pinned = appendAddress(pinned, nic.Address)
			}
		}
	}
	for _, container := range containers {
		ref := container.Spec.Network.SubnetRef
		if ref == "" {
			ref = DefaultSubnet
		}
		if ref == subnetName {
			pinned = appendAddress(pinned, container.Spec.Network.Address)
		}
	}
	return pinned
}

func pinnedAddresses(clusters []v1alpha1.KubernetesCluster, own string) []netip.Addr {
	var pinned []netip.Addr
	for _, other := range clusters {
		if other.Name != own {
			pinned = appendAddress(pinned, other.Spec.ControlPlane.Address)
		}
	}
	return pinned
}

func appendAddress(addresses []netip.Addr, text string) []netip.Addr {
	if addr, err := netip.ParseAddr(text); err == nil {
		return append(addresses, addr)
	}
	return addresses
}
