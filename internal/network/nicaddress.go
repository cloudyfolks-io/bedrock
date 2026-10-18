package network

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func CheckNICAddress(subnet v1alpha1.Subnet, address string) *Violation {
	prefix, prefixErr := netip.ParsePrefix(subnet.Spec.CIDR)
	addr, addrErr := netip.ParseAddr(address)
	if prefixErr != nil || addrErr != nil || !prefix.Contains(addr) {
		return &Violation{Reason: ReasonAddressOutsideSubnet, Message: fmt.Sprintf("the address %q is not inside %s", address, subnet.Spec.CIDR)}
	}
	gateway, gatewayErr := netip.ParseAddr(gatewayOf(subnet))
	switch {
	case gatewayErr == nil && gateway == addr:
		return reserved(address, "the gateway of the Subnet")
	case isNetworkOrBroadcast(prefix, addr):
		return reserved(address, "the network address or the broadcast address of the Subnet")
	case excluded(subnet.Spec.ExcludeIPs, addr):
		return reserved(address, "in an excluded range of the Subnet")
	}
	return nil
}

const fabricIPAddressField = "network.fabric-ip-address"

func fabricIPAddressKey(fabricSubnet, address string) string {
	return fabricSubnet + "/" + address
}

func fabricIPKeys(obj client.Object) []string {
	ip, ok := obj.(*unstructured.Unstructured)
	if !ok || fabric.IPAddress(ip) == "" {
		return nil
	}
	return []string{fabricIPAddressKey(ip.GetLabels()[labelFabricSubnet], fabric.IPAddress(ip))}
}

func IndexFabricIPs(ctx context.Context, indexer client.FieldIndexer) error {
	ip := &unstructured.Unstructured{}
	ip.SetGroupVersionKind(fabric.IPGVK)
	return indexer.IndexField(ctx, ip, fabricIPAddressField, fabricIPKeys)
}

func IPsOfAddress(ctx context.Context, c client.Reader, fabricSubnet, address string) ([]*unstructured.Unstructured, error) {
	return listObjects(ctx, c, fabric.IPGVK, client.MatchingFields{fabricIPAddressField: fabricIPAddressKey(fabricSubnet, address)})
}

func AddressHeldByAnother(ips []*unstructured.Unstructured, address, namespace, vm string) bool {
	return slices.ContainsFunc(ips, func(ip *unstructured.Unstructured) bool {
		ipNamespace, pod, _ := fabric.IPOwner(ip)
		return fabric.IPAddress(ip) == address && (ipNamespace != namespace || pod != vm)
	})
}
