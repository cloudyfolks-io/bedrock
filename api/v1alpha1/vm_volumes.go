package v1alpha1

func VolumeRefs(vm VirtualMachine) []string {
	refs := make([]string, 0, len(vm.Spec.Disks)+1)
	if vm.Spec.RootDisk.VolumeRef != "" {
		refs = append(refs, vm.Spec.RootDisk.VolumeRef)
	}
	for _, disk := range vm.Spec.Disks {
		refs = append(refs, disk.VolumeRef)
	}
	return refs
}
