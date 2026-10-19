package clusters

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/addons"
	"github.com/cloudyfolks-io/bedrock/internal/k0sversion"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const VersionsSetting = "clusters.versions"

var (
	clusterGVK      = schema.GroupVersionKind{Group: k0smotronGroup, Version: k0smotronVersion, Kind: "Cluster"}
	joinTokenGVK    = schema.GroupVersionKind{Group: k0smotronGroup, Version: k0smotronVersion, Kind: "JoinTokenRequest"}
	clusterListGVK  = schema.GroupVersionKind{Group: k0smotronGroup, Version: k0smotronVersion, Kind: "ClusterList"}
	errNoAddonKinds = errors.New("the k0smotron kinds are not served")
)

type Gate struct{ Reason, Message string }

func gateOf(reason, format string, args ...any) *Gate {
	return &Gate{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

type Resolver struct {
	Client    client.Client
	Reader    client.Reader
	FabricIPs client.Reader
}

type Resolved struct {
	Subnet       v1alpha1.Subnet
	Address      string
	AddressIsNew bool
}

type resolveStep func(ctx context.Context, cluster v1alpha1.KubernetesCluster, in Resolved) (Resolved, *Gate, error)

func notFound(err error) bool {
	return apierrors.IsNotFound(err) || meta.IsNoMatchError(err)
}

func (r Resolver) Resolve(ctx context.Context, cluster v1alpha1.KubernetesCluster) (Resolved, *Gate, error) {
	var in Resolved
	steps := []resolveStep{r.stepAddon, r.stepSubnet, r.stepSecurityGroups, r.stepCIDRs, r.stepVersion, r.stepAddress}
	for _, step := range steps {
		next, gate, err := step(ctx, cluster, in)
		if err != nil || gate != nil {
			return in, gate, err
		}
		in = next
	}
	return in, nil, nil
}

func (r Resolver) stepAddon(ctx context.Context, _ v1alpha1.KubernetesCluster, in Resolved) (Resolved, *Gate, error) {
	var addon v1alpha1.Addon
	err := r.Client.Get(ctx, client.ObjectKey{Name: addons.Clusters}, &addon)
	if err != nil && !notFound(err) {
		return in, nil, err
	}
	if err != nil || !addon.Spec.Enabled {
		return in, gateOf(ReasonAddonDisabled, "the addon %s is off: ask an administrator to turn it on", addons.Clusters), nil
	}
	if err := r.kindsServed(ctx); err != nil {
		if errors.Is(err, errNoAddonKinds) {
			return in, gateOf(ReasonAddonDisabled, "the k0smotron kinds are not served yet"), nil
		}
		return in, nil, err
	}
	return in, nil, nil
}

func (r Resolver) kindsServed(ctx context.Context) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(clusterListGVK)
	err := r.Client.List(ctx, list, client.Limit(1))
	if meta.IsNoMatchError(err) {
		return errNoAddonKinds
	}
	return err
}

func (r Resolver) stepSubnet(ctx context.Context, cluster v1alpha1.KubernetesCluster, in Resolved) (Resolved, *Gate, error) {
	name := SubnetName(cluster.Spec)
	var subnet v1alpha1.Subnet
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: name}, &subnet)
	if notFound(err) {
		return in, gateOf(ReasonSubnetNotFound, "the subnet %q does not exist", name), nil
	}
	if err != nil {
		return in, nil, err
	}
	if !meta.IsStatusConditionTrue(subnet.Status.Conditions, v1alpha1.ConditionReady) {
		return in, gateOf(ReasonSubnetNotReady, "the subnet %q is not ready", name), nil
	}
	var parent v1alpha1.Network
	err = r.Client.Get(ctx, client.ObjectKey{Name: subnet.Spec.NetworkRef}, &parent)
	if err != nil && !notFound(err) {
		return in, nil, err
	}
	if err != nil || parent.Status.VPC == "" {
		return in, gateOf(ReasonSubnetNotReady, "the network %q of the subnet %q is not ready", subnet.Spec.NetworkRef, name), nil
	}
	if !parent.Spec.EnableExternal || !meta.IsStatusConditionTrue(parent.Status.Conditions, network.ConditionExternalReady) {
		return in, gateOf(ReasonNoExternalPath, "the workers install k0s from the internet: the network %q needs an external connection", parent.Name), nil
	}
	in.Subnet = subnet
	return in, nil, nil
}

func GroupNames(spec v1alpha1.KubernetesClusterSpec) []string {
	names := slices.Clone(spec.ControlPlane.SecurityGroups)
	for _, worker := range spec.Workers {
		names = append(names, worker.SecurityGroups...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func (r Resolver) stepSecurityGroups(ctx context.Context, cluster v1alpha1.KubernetesCluster, in Resolved) (Resolved, *Gate, error) {
	for _, name := range GroupNames(cluster.Spec) {
		var group v1alpha1.SecurityGroup
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: name}, &group)
		if notFound(err) || (err == nil && !meta.IsStatusConditionTrue(group.Status.Conditions, v1alpha1.ConditionReady)) {
			return in, gateOf(ReasonSecurityGroupNotReady, "the security group %q is not ready", name), nil
		}
		if err != nil {
			return in, nil, err
		}
	}
	return in, nil, nil
}

func (r Resolver) stepCIDRs(ctx context.Context, cluster v1alpha1.KubernetesCluster, in Resolved) (Resolved, *Gate, error) {
	pod := netip.MustParsePrefix(cluster.Spec.Network.PodCIDR)
	service := netip.MustParsePrefix(cluster.Spec.Network.ServiceCIDR)
	if pod.Overlaps(service) {
		return in, gateOf(ReasonCIDROverlap, "%s", overlapMessage("podCIDR", pod, service)), nil
	}
	var subnets v1alpha1.SubnetList
	if err := r.Client.List(ctx, &subnets); err != nil {
		return in, nil, err
	}
	planes, err := r.platformPlanes(ctx)
	if err != nil {
		return in, nil, err
	}
	for name, prefix := range map[string]netip.Prefix{"podCIDR": pod, "serviceCIDR": service} {
		if other, clash := firstOverlap(prefix, sameNetworkSubnets(subnets.Items, in.Subnet.Spec.NetworkRef), planes); clash {
			return in, gateOf(ReasonCIDROverlap, "%s", overlapMessage(name, prefix, other)), nil
		}
	}
	return in, nil, nil
}

var planeSettings = []string{"network.load-balancer.cidr", "network.services.identity-range"}

func (r Resolver) platformPlanes(ctx context.Context) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, key := range planeSettings {
		var setting v1alpha1.Setting
		err := r.Client.Get(ctx, client.ObjectKey{Name: key}, &setting)
		if err != nil && !notFound(err) {
			return nil, err
		}
		value := setting.Spec.Value
		if def, known := settings.Lookup(key); value == "" && known {
			value = def.Default
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes, nil
}

func sameNetworkSubnets(subnets []v1alpha1.Subnet, networkRef string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, subnet := range subnets {
		if subnet.Spec.NetworkRef != networkRef {
			continue
		}
		if prefix, err := netip.ParsePrefix(subnet.Spec.CIDR); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

func firstOverlap(prefix netip.Prefix, others ...[]netip.Prefix) (netip.Prefix, bool) {
	for _, list := range others {
		for _, other := range list {
			if prefix.Overlaps(other) {
				return other, true
			}
		}
	}
	return netip.Prefix{}, false
}

func (r Resolver) AllowedVersions(ctx context.Context) ([]string, error) {
	var setting v1alpha1.Setting
	err := r.Client.Get(ctx, client.ObjectKey{Name: VersionsSetting}, &setting)
	if err != nil && !notFound(err) {
		return nil, err
	}
	value := setting.Spec.Value
	if value == "" {
		def, _ := settings.Lookup(VersionsSetting)
		value = def.Default
	}
	return k0sversion.ParseAllowed(value)
}

func (r Resolver) stepVersion(ctx context.Context, cluster v1alpha1.KubernetesCluster, in Resolved) (Resolved, *Gate, error) {
	allowed, err := r.AllowedVersions(ctx)
	if err != nil {
		return in, gateOf(ReasonVersionNotAllowed, "the Setting %s is not valid: %v", VersionsSetting, err), nil
	}
	if !k0sversion.Allowed(allowed, cluster.Spec.Version) {
		return in, gateOf(ReasonVersionNotAllowed, "the version %s is not in the Setting %s", cluster.Spec.Version, VersionsSetting), nil
	}
	return in, nil, nil
}

func (r Resolver) stepAddress(ctx context.Context, cluster v1alpha1.KubernetesCluster, in Resolved) (Resolved, *Gate, error) {
	if recorded := cluster.Status.ControlPlane.Address; recorded != "" {
		in.Address = recorded
		return in, nil, nil
	}
	taken, err := AddressReader{Clusters: r.Reader, IPs: r.FabricIPs}.Taken(ctx, cluster, in.Subnet.Name)
	if err != nil {
		return in, nil, err
	}
	var clusters v1alpha1.KubernetesClusterList
	if err := r.Reader.List(ctx, &clusters, client.InNamespace(cluster.Namespace)); err != nil {
		return in, nil, err
	}
	in.AddressIsNew = true
	if wanted := cluster.Spec.ControlPlane.Address; wanted != "" {
		return pinnedAddress(cluster, clusters.Items, in, wanted, taken)
	}
	taken = append(taken, pinnedAddresses(clusters.Items, cluster.Name)...)
	prefix, err := netip.ParsePrefix(in.Subnet.Spec.CIDR)
	if err != nil {
		return in, gateOf(ReasonSubnetNotReady, "the subnet %q has no valid cidr", in.Subnet.Name), nil
	}
	gateway, err := SubnetGateway(in.Subnet)
	if err != nil {
		return in, gateOf(ReasonSubnetNotReady, "the subnet %q has no valid gateway", in.Subnet.Name), nil
	}
	addr, err := NextFreeAddress(prefix, gateway, in.Subnet.Spec.ExcludeIPs, taken)
	var failure *AddressError
	if errors.As(err, &failure) {
		return in, gateOf(failure.Reason, "%s", failure.Message), nil
	}
	if err != nil {
		return in, nil, err
	}
	in.Address = addr.String()
	return in, nil, nil
}

func pinnedAddress(cluster v1alpha1.KubernetesCluster, clusters []v1alpha1.KubernetesCluster, in Resolved, wanted string, taken []netip.Addr) (Resolved, *Gate, error) {
	if violation := network.CheckNICAddress(in.Subnet, wanted); violation != nil {
		return in, &Gate{Reason: violation.Reason, Message: violation.Message}, nil
	}
	if slices.Contains(taken, netip.MustParseAddr(wanted)) {
		return in, gateOf(ReasonAddressInUse, "the address %s of the subnet %q is already used", wanted, in.Subnet.Name), nil
	}
	for index := range clusters {
		other := clusters[index]
		if other.Name != cluster.Name && other.Spec.ControlPlane.Address == wanted && network.Older(&other, &cluster) {
			return in, gateOf(ReasonAddressInUse, "the address %s is pinned by the older cluster %q", wanted, other.Name), nil
		}
	}
	in.Address = wanted
	return in, nil, nil
}
