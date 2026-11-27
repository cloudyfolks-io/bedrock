package network

import (
	"context"
	"fmt"
	"net/netip"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type PodRoute struct {
	Destination string `json:"dst"`
	Gateway     string `json:"gw"`
}

func ResolvePodNIC(ctx context.Context, reader client.Reader, cfg Config, nic NIC) (NIC, error) {
	if !cfg.TenantManagement.Enabled {
		nic.Class, nic.ManagementRoutes = "", nil
		return nic, nil
	}
	if nic.Class != NetworkClassWorkload && nic.Class != NetworkClassManaged {
		return nic, fmt.Errorf("the pod network class must be workload or managed")
	}
	var cluster v1alpha1.Cluster
	if err := reader.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return nic, err
	}
	ranges := ClusterRangesOf(cluster)
	if err := TenantManagementMissing(cfg, ranges); err != nil {
		return nic, err
	}
	if err := TenantManagementConflict(cfg, ranges); err != nil {
		return nic, err
	}
	prefix := cfg.TenantManagement.WorkloadCIDR
	destinations := []string{ranges.Management, ranges.Join, ranges.Pod}
	if nic.Class == NetworkClassManaged {
		prefix = cfg.TenantManagement.ManagedCIDR
		destinations = append(destinations, ranges.Service)
	}
	if !prefix.IsValid() {
		return nic, fmt.Errorf("the management subnet for class %s has no valid range", nic.Class)
	}
	nic.ManagementRoutes = make([]PodRoute, 0, len(destinations))
	for _, cidr := range destinations {
		route, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nic, fmt.Errorf("the management route %q is invalid: %w", cidr, err)
		}
		nic.ManagementRoutes = append(nic.ManagementRoutes, PodRoute{Destination: route.Masked().String(), Gateway: firstHost(prefix)})
	}
	return nic, nil
}
