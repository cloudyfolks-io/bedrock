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
	Client client.Client
	Reader client.Reader
}

func (r AddressReader) Taken(ctx context.Context, namespace string, subnetName string, own string) ([]netip.Addr, error) {
	ips := &unstructured.UnstructuredList{}
	ips.SetGroupVersionKind(fabric.IPGVK.GroupVersion().WithKind("IPList"))
	if err := r.Reader.List(ctx, ips, client.MatchingLabels{fabricSubnetLabel: network.FabricName(namespace, subnetName)}); err != nil {
		return nil, err
	}
	var clusters v1alpha1.KubernetesClusterList
	if err := r.Client.List(ctx, &clusters, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	return takenAddresses(ips.Items, clusters.Items, own), nil
}

func takenAddresses(ips []unstructured.Unstructured, clusters []v1alpha1.KubernetesCluster, own string) []netip.Addr {
	var taken []netip.Addr
	for index := range ips {
		if addr, err := netip.ParseAddr(fabric.IPAddress(&ips[index])); err == nil {
			taken = append(taken, addr)
		}
	}
	for _, cluster := range clusters {
		if cluster.Name == own {
			continue
		}
		if addr, err := netip.ParseAddr(cluster.Status.ControlPlane.Address); err == nil {
			taken = append(taken, addr)
		}
	}
	return taken
}
