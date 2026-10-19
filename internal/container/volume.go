package container

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const kindVirtualMachine = "VirtualMachine"

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
