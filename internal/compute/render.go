package compute

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const diskKeyLength = 20

type ResolvedVolume struct{ Name, Bus string }

type RenderInput struct {
	VirtualMachine v1alpha1.VirtualMachine
	Labels         map[string]string
	RootImage      *ResolvedImage
	ImageOS        *v1alpha1.ImageOS
	CDROMs         []ResolvedImage
	NICs           []NIC
	SSHKeys        []string
	Volumes        []ResolvedVolume
	UserData       string
	NetworkData    string
}

type Rendered struct {
	Secret         *corev1.Secret
	RootClaim      *unstructured.Unstructured
	CDROMClaims    []*unstructured.Unstructured
	VirtualMachine *unstructured.Unstructured
}

func DiskKey(volume string) string {
	sum := sha256.Sum256([]byte(volume))
	return hex.EncodeToString(sum[:])[:diskKeyLength]
}

func EffectivePreference(vm v1alpha1.VirtualMachine, image *v1alpha1.ImageOS) string {
	if vm.Spec.Preference != "" {
		return vm.Spec.Preference
	}
	if image != nil {
		return image.Preference
	}
	return ""
}

func Render(in RenderInput) (Rendered, error) {
	vm := in.VirtualMachine
	if len(in.NICs) == 0 {
		return Rendered{}, errors.New("a virtual machine needs at least one network")
	}
	userData, err := UserData(in.UserData, in.SSHKeys)
	if err != nil {
		return Rendered{}, err
	}
	affinity, err := ScopedAffinity(schedulingAffinity(vm.Spec.Scheduling))
	if err != nil {
		return Rendered{}, err
	}
	secret := CloudInitSecret(vm, in.Labels, userData, in.NetworkData)
	claims := make([]*unstructured.Unstructured, 0, len(in.CDROMs))
	for index, image := range in.CDROMs {
		claims = append(claims, CDROMClaim(vm, in.Labels, index, image))
	}
	kubeVirt, err := kubeVirtVM(in, affinity)
	if err != nil {
		return Rendered{}, err
	}
	return Rendered{
		Secret:         &secret,
		RootClaim:      RootClaim(vm, in.Labels, in.RootImage),
		CDROMClaims:    claims,
		VirtualMachine: kubeVirt,
	}, nil
}

func schedulingAffinity(scheduling *v1alpha1.VMScheduling) *corev1.Affinity {
	if scheduling == nil {
		return nil
	}
	return scheduling.Affinity
}

func kubeVirtVM(in RenderInput, affinity *corev1.Affinity) (*unstructured.Unstructured, error) {
	vm := in.VirtualMachine
	podAnnotations, podLabels := network.PodNetworkMetadata(in.NICs)
	templateLabels := mergeLabels(in.Labels, podLabels)
	templateSpec := map[string]any{
		"evictionStrategy": evictionStrategyOf(vm.Spec.EvictionStrategy),
		"domain":           domainOf(in),
		"networks":         networksOf(in.NICs),
		"volumes":          volumesOf(vm, in.Volumes, len(in.CDROMs), in.NetworkData),
	}
	if vm.Spec.Scheduling != nil && len(vm.Spec.Scheduling.NodeSelector) > 0 {
		templateSpec["nodeSelector"] = stringMap(vm.Spec.Scheduling.NodeSelector)
	}
	if affinity != nil {
		converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(affinity)
		if err != nil {
			return nil, fmt.Errorf("convert affinity: %w", err)
		}
		templateSpec["affinity"] = converted
	}
	spec := map[string]any{
		"runStrategy":  runStrategyOf(vm.Spec.Running),
		"instancetype": map[string]any{"kind": "VirtualMachineClusterInstancetype", "name": vm.Spec.InstanceType},
		"template": map[string]any{
			"metadata": map[string]any{"labels": stringMap(templateLabels), "annotations": stringMap(podAnnotations)},
			"spec":     templateSpec,
		},
	}
	if preference := EffectivePreference(vm, in.ImageOS); preference != "" {
		spec["preference"] = map[string]any{"kind": "VirtualMachineClusterPreference", "name": preference}
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata":   map[string]any{"name": vm.Name, "namespace": vm.Namespace},
		"spec":       spec,
	}}
	obj.SetLabels(copyOf(in.Labels))
	obj.SetOwnerReferences([]metav1.OwnerReference{OwnerRef(vm)})
	return obj, nil
}

func runStrategyOf(running bool) string {
	if running {
		return "Always"
	}
	return "Halted"
}

func evictionStrategyOf(spec string) string {
	if spec == "" {
		return v1alpha1.EvictionStrategyLiveMigrateIfPossible
	}
	return spec
}

func mergeLabels(first, second map[string]string) map[string]string {
	out := copyOf(first)
	for key, value := range second {
		out[key] = value
	}
	return out
}

func stringMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func domainOf(in RenderInput) map[string]any {
	vm := in.VirtualMachine
	domain := map[string]any{"devices": devicesOf(vm, in.Volumes, len(in.CDROMs), in.NICs)}
	if cpu := cpuOf(vm.Spec.CPU); cpu != nil {
		domain["cpu"] = cpu
	}
	if firmware := firmwareOf(vm.Spec.Firmware); firmware != nil {
		domain["firmware"] = firmware
	}
	if vm.Spec.Firmware != nil && vm.Spec.Firmware.SecureBoot {
		domain["features"] = map[string]any{"smm": map[string]any{"enabled": true}}
	}
	return domain
}

func firmwareOf(firmware *v1alpha1.VMFirmware) map[string]any {
	if firmware == nil {
		return nil
	}
	out := map[string]any{}
	if firmware.UEFI {
		out["bootloader"] = map[string]any{"efi": map[string]any{"secureBoot": firmware.SecureBoot}}
	}
	if firmware.UUID != "" {
		out["uuid"] = firmware.UUID
	}
	if firmware.Serial != "" {
		out["serial"] = firmware.Serial
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cpuOf(cpu *v1alpha1.VMCPU) map[string]any {
	if cpu == nil || (!cpu.Dedicated && cpu.Model == "") {
		return nil
	}
	out := map[string]any{}
	if cpu.Dedicated {
		out["dedicatedCpuPlacement"] = true
	}
	if cpu.Model != "" {
		out["model"] = cpu.Model
	}
	return out
}

func devicesOf(vm v1alpha1.VirtualMachine, volumes []ResolvedVolume, cdroms int, nics []NIC) map[string]any {
	devices := map[string]any{
		"autoattachPodInterface": false,
		"logSerialConsole":       true,
		"disks":                  disksOf(vm.Spec.Disks, volumes, cdroms),
		"interfaces":             interfacesOf(nics),
	}
	if vm.Spec.Firmware != nil && vm.Spec.Firmware.TPM {
		devices["tpm"] = map[string]any{"persistent": true}
	}
	if len(vm.Spec.HostDevices) > 0 {
		devices["hostDevices"] = hostDevicesOf(vm.Spec.HostDevices)
	}
	return devices
}

func disksOf(declared []v1alpha1.VMDisk, volumes []ResolvedVolume, cdroms int) []any {
	disks := []any{map[string]any{"name": "rootdisk", "disk": map[string]any{}, "bootOrder": int64(1)}}
	for index := 0; index < cdroms; index++ {
		disks = append(disks, map[string]any{
			"name":      cdromDiskName(index),
			"cdrom":     map[string]any{"readonly": true},
			"bootOrder": int64(index + 2),
		})
	}
	for _, volume := range volumes {
		key := DiskKey(volume.Name)
		disk := map[string]any{}
		if volume.Bus != "" {
			disk["bus"] = volume.Bus
		}
		disks = append(disks, map[string]any{"name": "vol-" + key, "disk": disk, "serial": serialOf(declared, volume.Name, key)})
	}
	return append(disks, map[string]any{"name": "cloudinit", "disk": map[string]any{}})
}

func serialOf(declared []v1alpha1.VMDisk, volume, fallback string) string {
	for _, disk := range declared {
		if disk.VolumeRef == volume && disk.Serial != "" {
			return disk.Serial
		}
	}
	return fallback
}

func cdromDiskName(index int) string {
	return fmt.Sprintf("cdrom-%d", index)
}

func interfacesOf(nics []NIC) []any {
	out := make([]any, 0, len(nics))
	for _, nic := range nics {
		iface := map[string]any{"name": nic.Subnet, "bridge": map[string]any{}}
		if nic.MAC != "" {
			iface["macAddress"] = nic.MAC
		}
		out = append(out, iface)
	}
	return out
}

func networksOf(nics []NIC) []any {
	attachments := network.Attachments(nics)
	out := make([]any, 0, len(nics))
	for index, nic := range nics {
		multus := map[string]any{"networkName": attachments[index]}
		if index == 0 {
			multus["default"] = true
		}
		out = append(out, map[string]any{"name": nic.Subnet, "multus": multus})
	}
	return out
}

func hostDevicesOf(devices []v1alpha1.VMHostDevice) []any {
	out := make([]any, 0, len(devices))
	for index, device := range devices {
		out = append(out, map[string]any{"name": fmt.Sprintf("hostdev-%d", index), "deviceName": device.ClaimRef})
	}
	return out
}

func volumesOf(vm v1alpha1.VirtualMachine, volumes []ResolvedVolume, cdroms int, networkData string) []any {
	out := []any{map[string]any{"name": "rootdisk", "dataVolume": map[string]any{"name": RootClaimName(vm.Name)}}}
	for index := 0; index < cdroms; index++ {
		out = append(out, map[string]any{"name": cdromDiskName(index), "dataVolume": map[string]any{"name": CDROMClaimName(vm.Name, index)}})
	}
	for _, volume := range volumes {
		out = append(out, map[string]any{
			"name":                  "vol-" + DiskKey(volume.Name),
			"persistentVolumeClaim": map[string]any{"claimName": volume.Name, "hotpluggable": true},
		})
	}
	return append(out, cloudInitVolume(vm.Name, networkData))
}

func cloudInitVolume(vm, networkData string) map[string]any {
	noCloud := map[string]any{"secretRef": map[string]any{"name": CloudInitSecretName(vm)}}
	if networkData != "" {
		noCloud["networkDataSecretRef"] = map[string]any{"name": CloudInitSecretName(vm)}
	}
	return map[string]any{"name": "cloudinit", "cloudInitNoCloud": noCloud}
}
