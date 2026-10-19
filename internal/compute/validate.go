package compute

import (
	"fmt"
	"maps"
	"slices"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const maxVMNameLength = 52

type Issue struct{ Field, Reason, Message string }

type ComputeLimits struct{ MaxCores, MaxMemoryMiB int32 }

func invalid(field, message string) Issue {
	return Issue{Field: field, Reason: ReasonInvalidSpec, Message: message}
}

func ValidateVirtualMachine(vm v1alpha1.VirtualMachine) []Issue {
	var issues []Issue
	issues = append(issues, nameIssues(vm.Name)...)
	issues = append(issues, imageIssues(vm.Spec)...)
	issues = append(issues, rootDiskIssues(vm.Spec.RootDisk)...)
	issues = append(issues, diskIssues(vm.Spec.Disks)...)
	issues = append(issues, cdromIssues(vm.Spec.CDROMs)...)
	issues = append(issues, networkIssues(vm.Spec.Networks)...)
	issues = append(issues, cloudInitIssues(vm.Spec.CloudInit)...)
	issues = append(issues, keyIssues(vm.Spec.SSHKeys)...)
	issues = append(issues, firmwareIssues(vm.Spec.Firmware)...)
	issues = append(issues, schedulingIssues(vm.Spec.Scheduling)...)
	return issues
}

func labelIssues(field, value string) []Issue {
	var issues []Issue
	for _, message := range validation.IsDNS1123Label(value) {
		issues = append(issues, invalid(field, message))
	}
	return issues
}

func subdomainIssues(field, value string) []Issue {
	var issues []Issue
	for _, message := range validation.IsDNS1123Subdomain(value) {
		issues = append(issues, invalid(field, message))
	}
	return issues
}

func nameIssues(name string) []Issue {
	issues := labelIssues("metadata.name", name)
	if len(name) > maxVMNameLength {
		issues = append(issues, invalid("metadata.name", fmt.Sprintf("must have at most %d characters", maxVMNameLength)))
	}
	return issues
}

func imageIssues(spec v1alpha1.VirtualMachineSpec) []Issue {
	if spec.Image == nil {
		return nil
	}
	return subdomainIssues("spec.image.name", spec.Image.Name)
}

func rootDiskIssues(root v1alpha1.VMRootDisk) []Issue {
	if root.VolumeRef == "" {
		return nil
	}
	return subdomainIssues("spec.rootDisk.volumeRef", root.VolumeRef)
}

func diskIssues(disks []v1alpha1.VMDisk) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	for index, disk := range disks {
		field := fmt.Sprintf("spec.disks[%d].volumeRef", index)
		issues = append(issues, subdomainIssues(field, disk.VolumeRef)...)
		if seen[disk.VolumeRef] {
			issues = append(issues, invalid(field, "a volume can be listed once"))
		}
		seen[disk.VolumeRef] = true
	}
	return issues
}

func cdromIssues(cdroms []v1alpha1.VMCDROM) []Issue {
	var issues []Issue
	for index, cdrom := range cdroms {
		issues = append(issues, subdomainIssues(fmt.Sprintf("spec.cdroms[%d].imageRef", index), cdrom.ImageRef)...)
	}
	return issues
}

func networkIssues(networks []v1alpha1.VMNetwork) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	for index, network := range networks {
		field := fmt.Sprintf("spec.networks[%d]", index)
		issues = append(issues, labelIssues(field+".subnetRef", network.SubnetRef)...)
		if seen[network.SubnetRef] {
			issues = append(issues, invalid(field+".subnetRef", "a subnet can be attached once"))
		}
		seen[network.SubnetRef] = true
		for group, name := range network.SecurityGroups {
			issues = append(issues, labelIssues(fmt.Sprintf("%s.securityGroups[%d]", field, group), name)...)
		}
		if network.ElasticIPRef != "" {
			issues = append(issues, labelIssues(field+".elasticIPRef", network.ElasticIPRef)...)
		}
	}
	return issues
}

func cloudInitIssues(ci *v1alpha1.VMCloudInit) []Issue {
	if ci == nil || ci.SecretRef == "" {
		return nil
	}
	issues := subdomainIssues("spec.cloudInit.secretRef", ci.SecretRef)
	if ci.UserData != "" || ci.NetworkData != "" {
		issues = append(issues, invalid("spec.cloudInit.secretRef", "secretRef excludes userData and networkData"))
	}
	return issues
}

func keyIssues(keys []string) []Issue {
	var issues []Issue
	for index, key := range keys {
		issues = append(issues, subdomainIssues(fmt.Sprintf("spec.sshKeys[%d]", index), key)...)
	}
	return issues
}

func firmwareIssues(firmware *v1alpha1.VMFirmware) []Issue {
	if firmware != nil && firmware.SecureBoot && !firmware.UEFI {
		return []Issue{invalid("spec.firmware.secureBoot", "secureBoot requires uefi")}
	}
	return nil
}

func schedulingIssues(scheduling *v1alpha1.VMScheduling) []Issue {
	if scheduling == nil {
		return nil
	}
	var issues []Issue
	for _, key := range slices.Sorted(maps.Keys(scheduling.NodeSelector)) {
		value := scheduling.NodeSelector[key]
		field := "spec.scheduling.nodeSelector[" + key + "]"
		for _, message := range validation.IsQualifiedName(key) {
			issues = append(issues, invalid(field, message))
		}
		for _, message := range validation.IsValidLabelValue(value) {
			issues = append(issues, invalid(field, message))
		}
	}
	if _, err := ScopedAffinity(scheduling.Affinity); err != nil {
		issues = append(issues, invalid("spec.scheduling.affinity", err.Error()))
	}
	return issues
}

func ComputeIssues(compute *v1alpha1.VMCompute, limits ComputeLimits) []Issue {
	if compute == nil {
		return nil
	}
	var issues []Issue
	if compute.Cores > limits.MaxCores {
		issues = append(issues, invalid("spec.compute.cores", fmt.Sprintf("must be at most %d", limits.MaxCores)))
	}
	if compute.MemoryMiB > limits.MaxMemoryMiB {
		issues = append(issues, invalid("spec.compute.memoryMiB", fmt.Sprintf("must be at most %d", limits.MaxMemoryMiB)))
	}
	return issues
}
