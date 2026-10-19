package container

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const (
	DefaultSubnetName   = "default"
	RuntimeSetting      = "container.runtime-class"
	DefaultRuntimeClass = "kata"
	RuntimeNone         = "none"
	filesystemClass     = "filesystem"
)

var fabricGroupGVK = schema.GroupVersionKind{Group: "fabric.cloudyfolks.io", Version: "v1", Kind: "SecurityGroup"}

type Gate struct{ Reason, Message string }

func gateOf(reason, format string, args ...any) *Gate {
	return &Gate{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

type Resolver struct {
	Client    client.Client
	Secrets   client.Reader
	FabricIPs client.Reader
}

type resolved struct {
	RenderInput
	subnet v1alpha1.Subnet
}

type resolveStep func(ctx context.Context, c v1alpha1.Container, in resolved) (resolved, *Gate, error)

func SubnetName(spec v1alpha1.ContainerSpec) string {
	if spec.Network.SubnetRef == "" {
		return DefaultSubnetName
	}
	return spec.Network.SubnetRef
}

func notFound(err error) bool {
	return apierrors.IsNotFound(err) || meta.IsNoMatchError(err)
}

func (r Resolver) Resolve(ctx context.Context, c v1alpha1.Container, labels map[string]string) (RenderInput, *Gate, error) {
	in := resolved{RenderInput: RenderInput{Container: c, Labels: labels}}
	steps := []resolveStep{r.stepSubnet, r.stepAddress, r.stepSecurityGroups, r.stepDefaultGroup, r.stepVolumes, r.stepRuntime, r.stepPullSecrets}
	for _, step := range steps {
		next, gate, err := step(ctx, c, in)
		if err != nil || gate != nil {
			return in.RenderInput, gate, err
		}
		in = next
	}
	return in.RenderInput, nil, nil
}

func (r Resolver) stepSubnet(ctx context.Context, c v1alpha1.Container, in resolved) (resolved, *Gate, error) {
	name := SubnetName(c.Spec)
	var subnet v1alpha1.Subnet
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: name}, &subnet)
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
	cfg, err := network.LoadConfig(ctx, r.Client)
	if err != nil {
		return in, nil, err
	}
	in.subnet = subnet
	in.NIC = network.NIC{Namespace: c.Namespace, Subnet: name, Address: c.Spec.Network.Address, SecurityGroups: slices.Clone(c.Spec.Network.SecurityGroups)}
	in.DNSServers = dnsServersOf(subnet, cfg.DNSServers)
	return in, nil, nil
}

func dnsServersOf(subnet v1alpha1.Subnet, fallback []string) []string {
	if len(subnet.Spec.DNSServers) > 0 {
		return slices.Clone(subnet.Spec.DNSServers)
	}
	return slices.Clone(fallback)
}

func (r Resolver) stepAddress(ctx context.Context, c v1alpha1.Container, in resolved) (resolved, *Gate, error) {
	address := c.Spec.Network.Address
	if address == "" {
		return in, nil, nil
	}
	if violation := network.CheckNICAddress(in.subnet, address); violation != nil {
		return in, &Gate{Reason: violation.Reason, Message: violation.Message}, nil
	}
	var containers v1alpha1.ContainerList
	if err := r.Client.List(ctx, &containers, client.InNamespace(c.Namespace)); err != nil {
		return in, nil, err
	}
	if other, held := pinnedBy(containers.Items, c); held {
		return in, gateOf(ReasonAddressInUse, "the address %s of the subnet %q is pinned by the container %q", address, in.NIC.Subnet, other), nil
	}
	pods, err := r.podNames(ctx, c)
	if err != nil {
		return in, nil, err
	}
	ips, err := network.IPsOfAddress(ctx, r.FabricIPs, network.FabricName(c.Namespace, in.NIC.Subnet), address)
	if err != nil {
		return in, nil, err
	}
	if heldByOther(ips, address, c.Namespace, pods) {
		return in, gateOf(ReasonAddressInUse, "the address %s of the subnet %q is already allocated to another pod", address, in.NIC.Subnet), nil
	}
	return in, nil, nil
}

func pinnedBy(containers []v1alpha1.Container, c v1alpha1.Container) (string, bool) {
	for index := range containers {
		other := containers[index]
		if other.Name != c.Name && SubnetName(other.Spec) == SubnetName(c.Spec) && other.Spec.Network.Address == c.Spec.Network.Address && network.Older(&other, &c) {
			return other.Name, true
		}
	}
	return "", false
}

func heldByOther(ips []*unstructured.Unstructured, address, namespace string, own []string) bool {
	return slices.ContainsFunc(ips, func(ip *unstructured.Unstructured) bool {
		ipNamespace, pod, _ := fabric.IPOwner(ip)
		return fabric.IPAddress(ip) == address && (ipNamespace != namespace || !slices.Contains(own, pod))
	})
}

func (r Resolver) podNames(ctx context.Context, c v1alpha1.Container) ([]string, error) {
	pods, err := network.PodsOfContainer(ctx, r.Client, c)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(pods))
	for i, pod := range pods {
		names[i] = pod.Name
	}
	return names, nil
}

func (r Resolver) stepSecurityGroups(ctx context.Context, c v1alpha1.Container, in resolved) (resolved, *Gate, error) {
	for _, name := range c.Spec.Network.SecurityGroups {
		var group v1alpha1.SecurityGroup
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: name}, &group)
		if notFound(err) || (err == nil && !meta.IsStatusConditionTrue(group.Status.Conditions, v1alpha1.ConditionReady)) {
			return in, gateOf(ReasonSecurityGroupNotReady, "the security group %q is not ready", name), nil
		}
		if err != nil {
			return in, nil, err
		}
	}
	return in, nil, nil
}

func (r Resolver) stepDefaultGroup(ctx context.Context, c v1alpha1.Container, in resolved) (resolved, *Gate, error) {
	if len(c.Spec.Network.SecurityGroups) > 0 {
		return in, nil, nil
	}
	name := network.DefaultSecurityGroupName(c.Namespace)
	group := &unstructured.Unstructured{}
	group.SetGroupVersionKind(fabricGroupGVK)
	err := r.Client.Get(ctx, client.ObjectKey{Name: name}, group)
	if notFound(err) {
		return in, gateOf(ReasonWaitingForDefaultGroup, "the default security group %q does not exist yet", name), nil
	}
	return in, nil, err
}

func (r Resolver) stepVolumes(ctx context.Context, c v1alpha1.Container, in resolved) (resolved, *Gate, error) {
	if len(c.Spec.Volumes) == 0 {
		return in, nil, nil
	}
	var vms v1alpha1.VirtualMachineList
	if err := r.Client.List(ctx, &vms, client.InNamespace(c.Namespace)); err != nil {
		return in, nil, err
	}
	var containers v1alpha1.ContainerList
	if err := r.Client.List(ctx, &containers, client.InNamespace(c.Namespace)); err != nil {
		return in, nil, err
	}
	for _, mount := range c.Spec.Volumes {
		var volume v1alpha1.Volume
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: mount.VolumeRef}, &volume)
		if notFound(err) {
			return in, gateOf(ReasonVolumeNotFound, "the volume %q does not exist", mount.VolumeRef), nil
		}
		if err != nil {
			return in, nil, err
		}
		if volume.Spec.StorageClass != filesystemClass {
			return in, gateOf(ReasonVolumeClassUnsupported, "the volume %q must have the storage class %q to be shared with a container", mount.VolumeRef, filesystemClass), nil
		}
		if holder, held := VolumeHolder(mount.VolumeRef, vms.Items, containers.Items); held && holder != (Holder{Kind: Kind, Name: c.Name}) {
			return in, gateOf(ReasonVolumeInUse, "the volume %q is used by %s", mount.VolumeRef, holder), nil
		}
	}
	return in, nil, nil
}

func (r Resolver) RuntimeClassName(ctx context.Context) (string, error) {
	var setting v1alpha1.Setting
	err := r.Client.Get(ctx, client.ObjectKey{Name: RuntimeSetting}, &setting)
	if err != nil && !notFound(err) {
		return "", err
	}
	return runtimeClassOf(setting.Spec.Value), nil
}

func runtimeClassOf(value string) string {
	switch value {
	case "":
		return DefaultRuntimeClass
	case RuntimeNone:
		return ""
	}
	return value
}

func (r Resolver) stepRuntime(ctx context.Context, _ v1alpha1.Container, in resolved) (resolved, *Gate, error) {
	name, err := r.RuntimeClassName(ctx)
	if err != nil {
		return in, nil, err
	}
	if name == "" {
		return in, nil, nil
	}
	err = r.Client.Get(ctx, client.ObjectKey{Name: name}, &nodev1.RuntimeClass{})
	if notFound(err) {
		return in, gateOf(ReasonRuntimeUnavailable, "the RuntimeClass %q does not exist: kata needs a host with KVM, or set the Setting %s to %s", name, RuntimeSetting, RuntimeNone), nil
	}
	if err != nil {
		return in, nil, err
	}
	in.RuntimeClass = name
	return in, nil, nil
}

func (r Resolver) stepPullSecrets(ctx context.Context, c v1alpha1.Container, in resolved) (resolved, *Gate, error) {
	reader := r.Secrets
	if reader == nil {
		reader = r.Client
	}
	for _, name := range c.Spec.ImagePullSecrets {
		err := reader.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: name}, &corev1.Secret{})
		if notFound(err) {
			return in, gateOf(ReasonPullSecretNotFound, "the pull secret %q does not exist", name), nil
		}
		if err != nil {
			return in, nil, err
		}
	}
	return in, nil, nil
}
