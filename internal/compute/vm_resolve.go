package compute

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/images"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

type resolveStep func(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error)

func gateOf(reason, format string, args ...any) *Gate {
	return &Gate{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

func notFound(err error) bool {
	return apierrors.IsNotFound(err) || meta.IsNoMatchError(err)
}

func EffectiveNetworks(vm v1alpha1.VirtualMachine) []v1alpha1.VMNetwork {
	if len(vm.Spec.Networks) == 0 {
		return []v1alpha1.VMNetwork{{SubnetRef: DefaultSubnetName}}
	}
	return vm.Spec.Networks
}

func VolumeOwner(vms []v1alpha1.VirtualMachine, volume string) string {
	owner := ""
	var oldest metav1.Time
	for _, vm := range vms {
		if !slices.Contains(v1alpha1.VolumeRefs(vm), volume) {
			continue
		}
		older := owner == "" || vm.CreationTimestamp.Before(&oldest) || (vm.CreationTimestamp.Equal(&oldest) && vm.Name < owner)
		if older {
			owner, oldest = vm.Name, vm.CreationTimestamp
		}
	}
	return owner
}

func (r *VirtualMachineReconciler) resolve(ctx context.Context, vm v1alpha1.VirtualMachine, labels map[string]string) (RenderInput, *Gate, error) {
	in := RenderInput{VirtualMachine: vm, Labels: labels}
	steps := []resolveStep{r.stepRootImage, r.stepRootDisk, r.stepRootVolume, r.stepCompute, r.stepEvictionStrategy, r.stepCDROMs, r.stepInstanceType, r.stepPreference, r.stepSubnets, r.stepAddresses, r.stepSecurityGroups, r.stepDefaultGroup, r.stepSSHKeys, r.stepCloudInit, r.stepVolumes}
	for _, step := range steps {
		next, gate, err := step(ctx, vm, in)
		if err != nil || gate != nil {
			return in, gate, err
		}
		in = next
	}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) stepRootImage(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	if vm.Spec.Image == nil {
		return in, nil, nil
	}
	image, imageOS, gate, err := lookupImage(ctx, r.Client, vm.Namespace, *vm.Spec.Image, v1alpha1.ImageTypeDisk)
	if err != nil || gate != nil {
		return in, gate, err
	}
	in.RootImage, in.ImageOS = &image, imageOS
	return in, nil, nil
}

func lookupImage(ctx context.Context, c client.Client, namespace string, ref v1alpha1.ImageRef, wantType string) (ResolvedImage, *v1alpha1.ImageOS, *Gate, error) {
	if ref.Scope == v1alpha1.ImageScopeProject {
		var img v1alpha1.Image
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, &img); err != nil {
			return ResolvedImage{}, nil, imageLookupGate(err, ref.Name), ignoreNotFound(err)
		}
		return resolveFound(img.Spec, img.Status, namespace, wantType)
	}
	var img v1alpha1.ClusterImage
	if err := c.Get(ctx, client.ObjectKey{Name: ref.Name}, &img); err != nil {
		return ResolvedImage{}, nil, imageLookupGate(err, ref.Name), ignoreNotFound(err)
	}
	return resolveFound(img.Spec, img.Status, images.CatalogNamespace, wantType)
}

func resolveFound(spec v1alpha1.ImageSpec, status v1alpha1.ImageStatus, namespace, wantType string) (ResolvedImage, *v1alpha1.ImageOS, *Gate, error) {
	image, err := ResolveImage(spec, status, namespace, wantType)
	if imageErr, ok := err.(ImageError); ok {
		return ResolvedImage{}, nil, &Gate{Reason: imageErr.Reason, Message: imageErr.Message}, nil
	}
	return image, spec.OS, nil, err
}

func imageLookupGate(err error, name string) *Gate {
	if notFound(err) {
		return gateOf(ReasonImageNotFound, "the image %q does not exist", name)
	}
	return nil
}

func ignoreNotFound(err error) error {
	if notFound(err) {
		return nil
	}
	return err
}

func (r *VirtualMachineReconciler) stepRootDisk(_ context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	if issue := RootDiskIssue(vm.Spec.RootDisk.SizeGi, in.RootImage); issue != nil {
		return in, &Gate{Reason: issue.Reason, Message: issue.Message}, nil
	}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) stepRootVolume(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	ref := vm.Spec.RootDisk.VolumeRef
	if ref == "" {
		return in, nil, nil
	}
	mounted, err := r.mountedRootClaim(ctx, vm)
	if err != nil {
		return in, nil, err
	}
	if mounted != "" && mounted != ref {
		in.RootVolume = &ResolvedVolume{Name: mounted, Bus: r.busOf(ctx, vm.Namespace, mounted)}
		in.PendingRootVolume = ref
		return in, nil, nil
	}
	volume, gate, err := r.attachableVolume(ctx, vm, ref)
	if err != nil || gate != nil {
		return in, gate, err
	}
	in.RootVolume = &ResolvedVolume{Name: ref, Bus: volume.Spec.Bus}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) mountedRootClaim(ctx context.Context, vm v1alpha1.VirtualMachine) (string, error) {
	instance := emptyOf(kubeVirtVMIGVK)
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(&vm), instance)
	if notFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return rootClaimNameIn(instance), nil
}

func (r *VirtualMachineReconciler) busOf(ctx context.Context, namespace, name string) string {
	var volume v1alpha1.Volume
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &volume); err != nil {
		return ""
	}
	return volume.Spec.Bus
}

func rootClaimNameIn(obj *unstructured.Unstructured) string {
	volumes, _, _ := unstructured.NestedSlice(obj.Object, "spec", "volumes")
	for _, item := range volumes {
		entry, ok := item.(map[string]any)
		if !ok || entry["name"] != "rootdisk" {
			continue
		}
		claim, _, _ := unstructured.NestedString(entry, "persistentVolumeClaim", "claimName")
		return claim
	}
	return ""
}

func (r *VirtualMachineReconciler) stepCDROMs(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	for _, cdrom := range vm.Spec.CDROMs {
		image, gate, err := r.isoNamed(ctx, vm.Namespace, cdrom.ImageRef)
		if err != nil || gate != nil {
			return in, gate, err
		}
		in.CDROMs = append(slices.Clone(in.CDROMs), image)
	}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) isoNamed(ctx context.Context, namespace, name string) (ResolvedImage, *Gate, error) {
	image, _, gate, err := lookupImage(ctx, r.Client, namespace, v1alpha1.ImageRef{Name: name, Scope: v1alpha1.ImageScopeProject}, v1alpha1.ImageTypeISO)
	if gate == nil || gate.Reason != ReasonImageNotFound {
		return image, gate, err
	}
	image, _, gate, err = lookupImage(ctx, r.Client, namespace, v1alpha1.ImageRef{Name: name, Scope: v1alpha1.ImageScopeCluster}, v1alpha1.ImageTypeISO)
	return image, gate, err
}

func (r *VirtualMachineReconciler) stepInstanceType(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	if vm.Spec.InstanceType == "" {
		return in, nil, nil
	}
	err := r.Client.Get(ctx, client.ObjectKey{Name: vm.Spec.InstanceType}, emptyOf(instanceTypeGVK))
	if notFound(err) {
		return in, gateOf(ReasonInstanceTypeNotFound, "the instance type %q does not exist", vm.Spec.InstanceType), nil
	}
	return in, nil, err
}

func (r *VirtualMachineReconciler) stepPreference(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	name := EffectivePreference(vm, in.ImageOS)
	if name == "" {
		return in, nil, nil
	}
	err := r.Client.Get(ctx, client.ObjectKey{Name: name}, emptyOf(preferenceGVK))
	if notFound(err) {
		return in, gateOf(ReasonPreferenceNotFound, "the preference %q does not exist", name), nil
	}
	return in, nil, err
}

func (r *VirtualMachineReconciler) stepSubnets(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	for _, network := range EffectiveNetworks(vm) {
		var subnet v1alpha1.Subnet
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: vm.Namespace, Name: network.SubnetRef}, &subnet)
		if notFound(err) || (err == nil && SubnetReady(subnet) != nil) {
			return in, gateOf(ReasonSubnetNotReady, "the subnet %q is not ready", network.SubnetRef), nil
		}
		if err != nil {
			return in, nil, err
		}
		in.NICs = append(slices.Clone(in.NICs), NIC{Namespace: vm.Namespace, Subnet: network.SubnetRef, MAC: network.MACAddress, Address: network.Address, SecurityGroups: slices.Clone(network.SecurityGroups)})
	}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) stepSecurityGroups(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	for _, nic := range in.NICs {
		for _, name := range nic.SecurityGroups {
			var group v1alpha1.SecurityGroup
			err := r.Client.Get(ctx, client.ObjectKey{Namespace: vm.Namespace, Name: name}, &group)
			if notFound(err) || (err == nil && !meta.IsStatusConditionTrue(group.Status.Conditions, v1alpha1.ConditionReady)) {
				return in, gateOf(ReasonSecurityGroupNotReady, "the security group %q is not ready", name), nil
			}
			if err != nil {
				return in, nil, err
			}
		}
	}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) stepDefaultGroup(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	if !slices.ContainsFunc(in.NICs, func(nic NIC) bool { return len(nic.SecurityGroups) == 0 }) {
		return in, nil, nil
	}
	name := network.DefaultSecurityGroupName(vm.Namespace)
	err := r.Client.Get(ctx, client.ObjectKey{Name: name}, emptyOf(fabricGroupGVK))
	if notFound(err) {
		return in, gateOf(ReasonWaitingForDefaultGroup, "the default security group %q does not exist yet", name), nil
	}
	return in, nil, err
}

func (r *VirtualMachineReconciler) stepSSHKeys(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	for _, name := range vm.Spec.SSHKeys {
		var key v1alpha1.SSHKey
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: vm.Namespace, Name: name}, &key)
		if notFound(err) || (err == nil && !meta.IsStatusConditionTrue(key.Status.Conditions, v1alpha1.ConditionReady)) {
			return in, gateOf(ReasonSSHKeyNotReady, "the SSH key %q is not ready", name), nil
		}
		if err != nil {
			return in, nil, err
		}
		normalized, parseErr := AuthorizedKey(key.Spec.PublicKey)
		if parseErr != nil {
			return in, gateOf(ReasonSSHKeyNotReady, "the SSH key %q is not valid", name), nil
		}
		in.SSHKeys = append(slices.Clone(in.SSHKeys), normalized)
	}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) stepCloudInit(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	var referenced *corev1.Secret
	if vm.Spec.CloudInit != nil && vm.Spec.CloudInit.SecretRef != "" {
		var secret corev1.Secret
		err := r.secretReader().Get(ctx, client.ObjectKey{Namespace: vm.Namespace, Name: vm.Spec.CloudInit.SecretRef}, &secret)
		if err != nil && !notFound(err) {
			return in, nil, err
		}
		if err == nil {
			referenced = &secret
		}
	}
	userData, networkData, err := CloudInitSources(vm.Spec.CloudInit, referenced)
	if err != nil {
		return in, gateOf(ReasonInvalidUserData, "%s", err.Error()), nil
	}
	if _, err := UserData(userData, in.SSHKeys); err != nil {
		return in, gateOf(ReasonInvalidUserData, "%s", err.Error()), nil
	}
	in.UserData, in.NetworkData = userData, networkData
	return in, nil, nil
}

func (r *VirtualMachineReconciler) stepVolumes(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	for _, disk := range vm.Spec.Disks {
		volume, gate, err := r.attachableVolume(ctx, vm, disk.VolumeRef)
		if err != nil || gate != nil {
			return in, gate, err
		}
		in.Volumes = append(slices.Clone(in.Volumes), ResolvedVolume{Name: disk.VolumeRef, Bus: effectiveBus(disk, volume)})
	}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) attachableVolume(ctx context.Context, vm v1alpha1.VirtualMachine, name string) (v1alpha1.Volume, *Gate, error) {
	var vms v1alpha1.VirtualMachineList
	if err := r.Client.List(ctx, &vms, client.InNamespace(vm.Namespace)); err != nil {
		return v1alpha1.Volume{}, nil, err
	}
	var volume v1alpha1.Volume
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: vm.Namespace, Name: name}, &volume)
	if notFound(err) {
		return volume, gateOf(ReasonVolumeNotFound, "the volume %q does not exist", name), nil
	}
	if err != nil {
		return volume, nil, err
	}
	if owner := VolumeOwner(vms.Items, name); owner != vm.Name {
		return volume, gateOf(ReasonVolumeInUse, "the volume %q is used by %q", name, owner), nil
	}
	gate, err := r.usable(ctx, volume)
	return volume, gate, err
}

func (r *VirtualMachineReconciler) usable(ctx context.Context, volume v1alpha1.Volume) (*Gate, error) {
	unusable := gateOf(ReasonVolumeNotUsable, "the volume %q is not ready to attach", volume.Name)
	if !meta.IsStatusConditionTrue(volume.Status.Conditions, v1alpha1.ConditionReady) {
		return unusable, nil
	}
	claim := emptyOf(dataVolumeGVK)
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(&volume), claim)
	if notFound(err) {
		return unusable, nil
	}
	if err != nil {
		return nil, err
	}
	if controllerUID(claim) != volume.UID {
		return unusable, nil
	}
	var pvc corev1.PersistentVolumeClaim
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(&volume), &pvc)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if owner := controllerUID(&pvc); owner != "" && owner != claim.GetUID() && owner != volume.UID {
		return unusable, nil
	}
	return nil, nil
}

func effectiveBus(disk v1alpha1.VMDisk, volume v1alpha1.Volume) string {
	if disk.Bus != "" {
		return disk.Bus
	}
	return volume.Spec.Bus
}

func issueMessage(issues []Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Field+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}
