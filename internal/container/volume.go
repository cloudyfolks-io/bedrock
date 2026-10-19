package container

import (
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const kindVirtualMachine = "VirtualMachine"

var virtualMachineInstanceListGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstanceList"}

type Holder struct{ Kind, Name string }

func (h Holder) String() string {
	return h.Kind + "/" + h.Name
}

func VolumeHolder(volume string, vms []v1alpha1.VirtualMachine, containers []v1alpha1.Container) (Holder, bool) {
	var holder Holder
	var oldest metav1.Object
	consider := func(candidate Holder, object metav1.Object) {
		if oldest == nil || network.Older(object, oldest) || (network.CompareAge(object, oldest) == 0 && candidate.String() < holder.String()) {
			holder, oldest = candidate, object
		}
	}
	for index := range vms {
		if slices.Contains(v1alpha1.VolumeRefs(vms[index]), volume) {
			consider(Holder{Kind: kindVirtualMachine, Name: vms[index].Name}, &vms[index])
		}
	}
	for index := range containers {
		if slices.ContainsFunc(containers[index].Spec.Volumes, func(mount v1alpha1.ContainerVolume) bool { return mount.VolumeRef == volume }) {
			consider(Holder{Kind: Kind, Name: containers[index].Name}, &containers[index])
		}
	}
	return holder, oldest != nil
}

func MountingInstance(ctx context.Context, c client.Reader, namespace, claim string) (string, error) {
	instances := &unstructured.UnstructuredList{}
	instances.SetGroupVersionKind(virtualMachineInstanceListGVK)
	if err := c.List(ctx, instances, client.InNamespace(namespace)); err != nil {
		if meta.IsNoMatchError(err) {
			return "", nil
		}
		return "", client.IgnoreNotFound(err)
	}
	for index := range instances.Items {
		if mountsClaim(&instances.Items[index], claim) {
			return instances.Items[index].GetName(), nil
		}
	}
	return "", nil
}

func mountsClaim(instance *unstructured.Unstructured, claim string) bool {
	volumes, _, _ := unstructured.NestedSlice(instance.Object, "spec", "volumes")
	for _, item := range volumes {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(entry, "persistentVolumeClaim", "claimName"); name == claim {
			return true
		}
	}
	return false
}
