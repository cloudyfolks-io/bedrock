package settings

const (
	StorageOSDMemory       = "storage.osd-memory"
	StorageOSDMemoryTarget = "storage.osd-memory-target"
	StorageOSDCPU          = "storage.osd-cpu"
	StorageMonMemory       = "storage.mon-memory"
	StorageMgrMemory       = "storage.mgr-memory"
	StorageMDSMemory       = "storage.mds-memory"
	StorageRGWMemory       = "storage.rgw-memory"
)

func storageSizingDefinitions() []Definition {
	return []Definition{
		{StorageOSDMemory, "5Gi", "Memory request and limit of every Ceph OSD pod. Request equal to limit gives the pod the Guaranteed QoS class.", positiveQuantity},
		{StorageOSDMemoryTarget, "4Gi", "osd_memory_target of every Ceph OSD. It must be lower than storage.osd-memory so that the OSD does not reach its limit.", positiveQuantity},
		{StorageOSDCPU, "2", "CPU request and limit of every Ceph OSD pod.", positiveQuantity},
		{StorageMonMemory, "2Gi", "Memory request of every Ceph mon pod. The limit is 1.5 times the request.", positiveQuantity},
		{StorageMgrMemory, "1Gi", "Memory request of every Ceph mgr pod. The limit is twice the request.", positiveQuantity},
		{StorageMDSMemory, "2Gi", "Memory request of every CephFS metadata server pod. The limit is twice the request.", positiveQuantity},
		{StorageRGWMemory, "2Gi", "Memory request of every object gateway pod. The limit is twice the request.", positiveQuantity},
	}
}
