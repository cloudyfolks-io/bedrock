package fabrictest

import (
	"context"
	"net/netip"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func Auto(ctx context.Context, c client.Client, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		step(ctx, c)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func each(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, visit func(*unstructured.Unstructured)) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := c.List(ctx, list); err != nil {
		return
	}
	for i := range list.Items {
		visit(&list.Items[i])
	}
}

func step(ctx context.Context, c client.Client) {
	each(ctx, c, fabric.ProviderNetworkGVK, func(u *unstructured.Unstructured) { _ = MarkProviderNetworkReady(ctx, c, u.GetName()) })
	each(ctx, c, fabric.VpcGVK, func(u *unstructured.Unstructured) {
		_ = MarkVpcStandby(ctx, c, u.GetName())
		subnets, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "extraExternalSubnets")
		for _, subnet := range subnets {
			ensureRouterPort(ctx, c, u.GetName(), subnet)
		}
	})
	each(ctx, c, fabric.SubnetGVK, func(u *unstructured.Unstructured) { _ = MarkSubnetReady(ctx, c, u.GetName()) })
	allocate(ctx, c)
	each(ctx, c, fabric.OvnFipGVK, func(u *unstructured.Unstructured) { _ = MarkFipReady(ctx, c, u.GetName()) })
	each(ctx, c, fabric.RouterLBRuleGVK, func(u *unstructured.Unstructured) {
		_ = MarkRouterLBRuleProgrammed(ctx, c, u.GetName(), "rlr-"+u.GetName())
	})
	each(ctx, c, fabric.DNSZoneGVK, func(u *unstructured.Unstructured) { _ = MarkDNSZoneReady(ctx, c, u.GetName()) })
	each(ctx, c, fabric.SecurityGroupGVK, func(u *unstructured.Unstructured) { _ = MarkSecurityGroupProgrammed(ctx, c, u.GetName()) })
}

func ensureRouterPort(ctx context.Context, c client.Client, vpc, subnet string) {
	name := vpc + "-" + subnet
	if _, err := get(ctx, c, fabric.OvnEipGVK, name); errors.IsNotFound(err) {
		_ = c.Create(ctx, fabric.OvnEip(fabric.Meta{Name: name}, subnet, ""))
	}
}

func get(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u, c.Get(ctx, client.ObjectKey{Name: name}, u)
}

func allocate(ctx context.Context, c client.Client) {
	used := map[netip.Addr]bool{}
	var pending []*unstructured.Unstructured
	each(ctx, c, fabric.OvnEipGVK, func(u *unstructured.Unstructured) {
		if address := fabric.EipAddress(u); address != "" {
			if addr, err := netip.ParseAddr(address); err == nil {
				used[addr] = true
			}
			return
		}
		pending = append(pending, u)
	})
	for _, u := range pending {
		subnet, _, _ := unstructured.NestedString(u.Object, "spec", "externalSubnet")
		requested, _, _ := unstructured.NestedString(u.Object, "spec", "v4Ip")
		address := requested
		if address == "" {
			address = nextFree(ctx, c, subnet, used)
		}
		if address == "" {
			continue
		}
		if addr, err := netip.ParseAddr(address); err == nil {
			used[addr] = true
		}
		_ = MarkEipAllocated(ctx, c, u.GetName(), address)
	}
}

func nextFree(ctx context.Context, c client.Client, subnetName string, used map[netip.Addr]bool) string {
	subnet, err := get(ctx, c, fabric.SubnetGVK, subnetName)
	if err != nil {
		return ""
	}
	cidr, _, _ := unstructured.NestedString(subnet.Object, "spec", "cidrBlock")
	gateway, _, _ := unstructured.NestedString(subnet.Object, "spec", "gateway")
	excludes, _, _ := unstructured.NestedStringSlice(subnet.Object, "spec", "excludeIps")
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return ""
	}
	for addr := prefix.Masked().Addr().Next(); prefix.Contains(addr); addr = addr.Next() {
		if addr.String() != gateway && !used[addr] && !excluded(excludes, addr) {
			return addr.String()
		}
	}
	return ""
}

func excluded(ranges []string, addr netip.Addr) bool {
	for _, entry := range ranges {
		first, last, err := settings.ParseAddressRange(entry)
		if err == nil && !addr.Less(first) && !last.Less(addr) {
			return true
		}
	}
	return false
}
