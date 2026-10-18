package compute

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	kubeVirtVMGVK        = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"}
	kubeVirtVMIGVK       = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}
	kubeVirtMigrationGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstanceMigration"}
	vmSnapshotGVK        = schema.GroupVersionKind{Group: "snapshot.kubevirt.io", Version: "v1beta1", Kind: "VirtualMachineSnapshot"}
	instanceTypeGVK      = schema.GroupVersionKind{Group: "instancetype.kubevirt.io", Version: "v1beta1", Kind: "VirtualMachineClusterInstancetype"}
	preferenceGVK        = schema.GroupVersionKind{Group: "instancetype.kubevirt.io", Version: "v1beta1", Kind: "VirtualMachineClusterPreference"}
	dataVolumeGVK        = schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume"}
	csiSnapshotGVK       = schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshot"}
	fabricGroupGVK       = schema.GroupVersionKind{Group: "fabric.cloudyfolks.io", Version: "v1", Kind: "SecurityGroup"}
	fabricIPGVK          = schema.GroupVersionKind{Group: "fabric.cloudyfolks.io", Version: "v1", Kind: "IP"}
	csiSnapshotClassGVK  = schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshotClass"}
)

func emptyOf(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return obj
}
