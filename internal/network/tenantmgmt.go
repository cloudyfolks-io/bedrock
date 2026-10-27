package network

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/config"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const (
	TenantWorkloadSubnet = "tenant-workload"
	TenantManagedSubnet  = "tenant-managed"

	VPCAttachmentName      = "vpc"
	VPCAttachmentNamespace = "kube-system"

	UsabilityTenantManagement = "tenant-mgmt"

	tenantManagementVPC = "ovn-cluster"
	tenantManagementMTU = 1500

	vpcAttachmentProvider = "vpc.kube-system.fabric"
	vpcAttachmentSocket   = "/run/openvswitch/fabric-daemon.sock"

	aclFromLport       = "from-lport"
	aclToLport         = "to-lport"
	aclDrop            = "drop"
	aclAllowRelated    = "allow-related"
	aclPlatformDrop    = 3500
	aclTenantDrop      = 2500
	aclPlatformAllowed = 1000
)

type ClusterRanges struct {
	Management string
	Join       string
	Pod        string
	Service    string
}

func ClusterRangesOf(cluster v1alpha1.Cluster) ClusterRanges {
	return ClusterRanges{
		Management: cluster.Spec.ManagementCIDR,
		Join:       cmp.Or(cluster.Spec.JoinCIDR, config.DefaultJoinCIDR),
		Pod:        config.DefaultPodCIDR,
		Service:    config.DefaultServiceCIDR,
	}
}

func DefaultClusterRanges() ClusterRanges {
	return ClusterRangesOf(v1alpha1.Cluster{})
}

func PlatformRanges(cfg Config, cluster ClusterRanges) []string {
	ranges := []string{cluster.Management, cluster.Join, cluster.Pod}
	for _, pn := range cfg.ProviderNetworks {
		if pn.Role == v1alpha1.ProviderRoleTransit || pn.Role == v1alpha1.ProviderRoleServices || pn.Name == cfg.BGP.Transit {
			ranges = append(ranges, pn.CIDR)
		}
	}
	ranges = slices.DeleteFunc(ranges, func(cidr string) bool { return cidr == "" })
	slices.Sort(ranges)
	return slices.Compact(ranges)
}

func TenantManagementConflict(cfg Config, cluster ClusterRanges) error {
	var others []ReservedRange
	for _, pn := range cfg.ProviderNetworks {
		others = append(others, ReservedRange{Name: "provider network " + pn.Name, CIDR: pn.CIDR})
	}
	for name, cidr := range map[string]string{"management": cluster.Management, "join subnet": cluster.Join, "pod": cluster.Pod, "service": cluster.Service, "peering": cfg.PeeringRange} {
		if cidr != "" {
			others = append(others, ReservedRange{Name: name, CIDR: cidr})
		}
	}
	slices.SortFunc(others, func(a, b ReservedRange) int { return cmp.Compare(a.Name, b.Name) })
	for _, prefix := range []netip.Prefix{cfg.TenantManagement.WorkloadCIDR, cfg.TenantManagement.ManagedCIDR} {
		if other, found := firstOverlap(prefix, others); found {
			return fmt.Errorf("the tenant management range %s overlaps the %s range %s", prefix, other.Name, other.CIDR)
		}
	}
	return nil
}

func firstHost(prefix netip.Prefix) string {
	return prefix.Masked().Addr().Next().String()
}

func addressSet(cidrs ...string) string {
	return "{" + strings.Join(cidrs, ", ") + "}"
}

func platformAllowACL(platform []string) fabric.ACL {
	return fabric.ACL{Direction: aclToLport, Priority: aclPlatformAllowed, Match: "ip4.src == " + addressSet(platform...), Action: aclAllowRelated}
}

func noIPv6ACL() fabric.ACL {
	return fabric.ACL{Direction: aclToLport, Priority: aclTenantDrop, Match: "ip6", Action: aclDrop}
}

func workloadACLs(workload netip.Prefix, platform []string, service string) []fabric.ACL {
	gateway := firstHost(workload)
	cidr := workload.Masked().String()
	blocked := addressSet(append(slices.Clone(platform), service)...)
	return []fabric.ACL{
		{Direction: aclFromLport, Priority: aclPlatformDrop, Match: "ct.new && ip4.dst == " + blocked, Action: aclDrop},
		{Direction: aclFromLport, Priority: aclPlatformDrop, Match: "arp.op == 1 && arp.tpa != " + gateway, Action: aclDrop},
		{Direction: aclToLport, Priority: aclTenantDrop, Match: fmt.Sprintf("ip4.src == %s && ip4.dst == %s && ip4.src != %s && ip4.dst != %s", cidr, cidr, gateway, gateway), Action: aclDrop},
		noIPv6ACL(),
		platformAllowACL(platform),
	}
}

func managedACLs(platform []string) []fabric.ACL {
	return []fabric.ACL{
		{Direction: aclFromLport, Priority: aclPlatformDrop, Match: "ct.new && ip4.dst == " + addressSet(platform...), Action: aclDrop},
		noIPv6ACL(),
		platformAllowACL(platform),
	}
}

func tenantManagementSubnet(name string, cidr netip.Prefix, acls []fabric.ACL) *unstructured.Unstructured {
	meta := underlayMeta("Subnet", name)
	meta.Labels[LabelUsability] = UsabilityTenantManagement
	return fabric.Subnet(meta, fabric.SubnetSpec{Vpc: tenantManagementVPC, CIDR: cidr.Masked().String(), Gateway: firstHost(cidr), MTU: tenantManagementMTU, ACLs: acls})
}

func TenantManagementSubnets(cfg Config, cluster ClusterRanges) []*unstructured.Unstructured {
	if !cfg.TenantManagement.Enabled {
		return nil
	}
	platform := PlatformRanges(cfg, cluster)
	return []*unstructured.Unstructured{
		tenantManagementSubnet(TenantWorkloadSubnet, cfg.TenantManagement.WorkloadCIDR, workloadACLs(cfg.TenantManagement.WorkloadCIDR, platform, cluster.Service)),
		tenantManagementSubnet(TenantManagedSubnet, cfg.TenantManagement.ManagedCIDR, managedACLs(platform)),
	}
}

func VPCAttachment() *unstructured.Unstructured {
	config, _ := json.Marshal(map[string]string{"cniVersion": "0.3.1", "type": "fabric", "server_socket": vpcAttachmentSocket, "provider": vpcAttachmentProvider})
	nad := newObject(attachmentGVK, VPCAttachmentName)
	nad.SetNamespace(VPCAttachmentNamespace)
	labels := underlayMeta("NetworkAttachmentDefinition", VPCAttachmentName).Labels
	nad.SetLabels(labels)
	nad.Object["spec"] = map[string]any{"config": string(config)}
	return nad
}
