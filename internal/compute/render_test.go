package compute

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func renderFixture() RenderInput {
	vm := fixtureVM("acme-web", "web-1")
	vm.UID = types.UID("vm-uid")
	vm.Spec.CDROMs = []v1alpha1.VMCDROM{{ImageRef: "install-iso"}}
	vm.Spec.Networks = []v1alpha1.VMNetwork{{SubnetRef: "default", SecurityGroups: []string{"web"}, MACAddress: "52:54:00:12:34:56"}}
	vm.Spec.EvictionStrategy = v1alpha1.EvictionStrategyLiveMigrate
	vm.Spec.Firmware = &v1alpha1.VMFirmware{UEFI: true, SecureBoot: true, TPM: true}
	vm.Spec.CPU = &v1alpha1.VMCPU{Dedicated: true, Model: "host-model"}
	vm.Spec.HostDevices = []v1alpha1.VMHostDevice{{ClaimRef: "nvidia.com/GA102"}}
	vm.Spec.Scheduling = &v1alpha1.VMScheduling{
		NodeSelector: map[string]string{"disk": "ssd"},
		Affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			TopologyKey:   "kubernetes.io/hostname",
		}}}},
	}
	return RenderInput{
		VirtualMachine: vm,
		Labels:         v1alpha1.TenantLabels("VirtualMachine", "web-1", "acme", "web"),
		RootImage:      &ResolvedImage{Namespace: "bedrock-images", DataSource: "ubuntu-24-04", SizeGi: 3},
		CDROMs:         []ResolvedImage{{Namespace: "bedrock-images", DataSource: "iso-1", SizeGi: 1}},
		NICs:           []NIC{{Namespace: "acme-web", Subnet: "default", SecurityGroups: []string{"web"}, MAC: "52:54:00:12:34:56"}},
		SSHKeys:        []string{"ssh-ed25519 AAAAk1"},
		Volumes:        []ResolvedVolume{{Name: "data1", Bus: "scsi"}},
		UserData:       "#cloud-config\nhostname: web-1\n",
	}
}

func mustRender(t *testing.T, in RenderInput) Rendered {
	t.Helper()
	out, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func templateSpec(t *testing.T, out Rendered) map[string]any {
	t.Helper()
	spec, found, _ := unstructured.NestedMap(out.VirtualMachine.Object, "spec", "template", "spec")
	if !found {
		t.Fatal("no template spec")
	}
	return spec
}

func list(t *testing.T, in map[string]any, path ...string) []any {
	t.Helper()
	out, found, err := unstructured.NestedSlice(in, path...)
	if !found || err != nil {
		t.Fatalf("no list at %v: %v", path, err)
	}
	return out
}

func TestDiskKey(t *testing.T) {
	if got := DiskKey("data1"); got != "5b41362bc82b7f3d56ed" {
		t.Fatal(got)
	}
}

func TestRenderDisksAndVolumes(t *testing.T) {
	spec := templateSpec(t, mustRender(t, renderFixture()))
	disks := list(t, spec, "domain", "devices", "disks")
	wantDisks := []any{
		map[string]any{"name": "rootdisk", "disk": map[string]any{}, "bootOrder": int64(1)},
		map[string]any{"name": "cdrom-0", "cdrom": map[string]any{"readonly": true}, "bootOrder": int64(2)},
		map[string]any{"name": "vol-5b41362bc82b7f3d56ed", "disk": map[string]any{"bus": "scsi"}, "serial": "5b41362bc82b7f3d56ed"},
		map[string]any{"name": "cloudinit", "disk": map[string]any{}},
	}
	if !reflect.DeepEqual(disks, wantDisks) {
		t.Fatalf("disks %+v", disks)
	}
	wantVolumes := []any{
		map[string]any{"name": "rootdisk", "dataVolume": map[string]any{"name": "web-1-root"}},
		map[string]any{"name": "cdrom-0", "dataVolume": map[string]any{"name": "web-1-cdrom-0"}},
		map[string]any{"name": "vol-5b41362bc82b7f3d56ed", "persistentVolumeClaim": map[string]any{"claimName": "data1", "hotpluggable": true}},
		map[string]any{"name": "cloudinit", "cloudInitNoCloud": map[string]any{"userDataSecretRef": map[string]any{"name": "web-1-cloudinit"}}},
	}
	if got := list(t, spec, "volumes"); !reflect.DeepEqual(got, wantVolumes) {
		t.Fatalf("volumes %+v", got)
	}
}

func TestRenderLeavesBusAndModelToThePreference(t *testing.T) {
	spec := templateSpec(t, mustRender(t, renderFixture()))
	root := list(t, spec, "domain", "devices", "disks")[0].(map[string]any)
	if _, found := root["disk"].(map[string]any)["bus"]; found {
		t.Fatalf("root disk %+v", root)
	}
	iface := list(t, spec, "domain", "devices", "interfaces")[0].(map[string]any)
	if _, found := iface["model"]; found {
		t.Fatalf("interface %+v", iface)
	}
}

func TestRenderNetworks(t *testing.T) {
	in := renderFixture()
	in.NICs = append(in.NICs, NIC{Namespace: "acme-web", Subnet: "db"})
	out := mustRender(t, in)
	spec := templateSpec(t, out)
	wantNetworks := []any{
		map[string]any{"name": "default", "multus": map[string]any{"networkName": "acme-web/default", "default": true}},
		map[string]any{"name": "db", "multus": map[string]any{"networkName": "acme-web/db"}},
	}
	if got := list(t, spec, "networks"); !reflect.DeepEqual(got, wantNetworks) {
		t.Fatalf("networks %+v", got)
	}
	wantInterfaces := []any{
		map[string]any{"name": "default", "bridge": map[string]any{}, "macAddress": "52:54:00:12:34:56"},
		map[string]any{"name": "db", "bridge": map[string]any{}},
	}
	if got := list(t, spec, "domain", "devices", "interfaces"); !reflect.DeepEqual(got, wantInterfaces) {
		t.Fatalf("interfaces %+v", got)
	}
	if auto, _, _ := unstructured.NestedBool(spec, "domain", "devices", "autoattachPodInterface"); auto {
		t.Fatal("autoattachPodInterface must be false")
	}
	annotations, labels := PodNetworkMetadata(in.NICs)
	gotAnnotations, _, _ := unstructured.NestedStringMap(out.VirtualMachine.Object, "spec", "template", "metadata", "annotations")
	gotLabels, _, _ := unstructured.NestedStringMap(out.VirtualMachine.Object, "spec", "template", "metadata", "labels")
	if !reflect.DeepEqual(gotAnnotations, annotations) || !reflect.DeepEqual(gotLabels, mergeLabels(in.Labels, labels)) {
		t.Fatalf("annotations %+v labels %+v", gotAnnotations, gotLabels)
	}
}

func TestRenderNetworkDataReference(t *testing.T) {
	in := renderFixture()
	spec := templateSpec(t, mustRender(t, in))
	if _, found, _ := unstructured.NestedMap(list(t, spec, "volumes")[3].(map[string]any), "cloudInitNoCloud", "networkDataSecretRef"); found {
		t.Fatal("no network data, no reference")
	}
	in.NetworkData = "version: 2\n"
	spec = templateSpec(t, mustRender(t, in))
	if name, _, _ := unstructured.NestedString(list(t, spec, "volumes")[3].(map[string]any), "cloudInitNoCloud", "networkDataSecretRef", "name"); name != "web-1-cloudinit" {
		t.Fatalf("name %q", name)
	}
}

func TestRenderRunStrategy(t *testing.T) {
	in := renderFixture()
	for running, want := range map[bool]string{true: "Always", false: "Halted"} {
		in.VirtualMachine.Spec.Running = running
		out := mustRender(t, in)
		if got, _, _ := unstructured.NestedString(out.VirtualMachine.Object, "spec", "runStrategy"); got != want {
			t.Errorf("running=%v gave %q", running, got)
		}
		if _, found, _ := unstructured.NestedFieldNoCopy(out.VirtualMachine.Object, "spec", "running"); found {
			t.Error("the deprecated running field must not be set")
		}
		if _, found, _ := unstructured.NestedFieldNoCopy(out.VirtualMachine.Object, "spec", "updateVolumesStrategy"); found {
			t.Error("updateVolumesStrategy must not be set")
		}
	}
}

func TestRenderInstanceTypeAndPreference(t *testing.T) {
	in := renderFixture()
	out := mustRender(t, in)
	instance, _, _ := unstructured.NestedMap(out.VirtualMachine.Object, "spec", "instancetype")
	preference, _, _ := unstructured.NestedMap(out.VirtualMachine.Object, "spec", "preference")
	if !reflect.DeepEqual(instance, map[string]any{"kind": "VirtualMachineClusterInstancetype", "name": "u1.medium"}) || !reflect.DeepEqual(preference, map[string]any{"kind": "VirtualMachineClusterPreference", "name": "ubuntu"}) {
		t.Fatalf("%+v %+v", instance, preference)
	}
	in.VirtualMachine.Spec.Preference = ""
	in.ImageOS = &v1alpha1.ImageOS{Preference: "windows.2k22"}
	if name, _, _ := unstructured.NestedString(mustRender(t, in).VirtualMachine.Object, "spec", "preference", "name"); name != "windows.2k22" {
		t.Fatalf("preference %q", name)
	}
	in.ImageOS = nil
	if _, found, _ := unstructured.NestedMap(mustRender(t, in).VirtualMachine.Object, "spec", "preference"); found {
		t.Fatal("no preference expected")
	}
}

func TestEffectivePreference(t *testing.T) {
	vm := v1alpha1.VirtualMachine{Spec: v1alpha1.VirtualMachineSpec{Preference: "own"}}
	if got := EffectivePreference(vm, &v1alpha1.ImageOS{Preference: "image"}); got != "own" {
		t.Error(got)
	}
	vm.Spec.Preference = ""
	if got := EffectivePreference(vm, &v1alpha1.ImageOS{Preference: "image"}); got != "image" {
		t.Error(got)
	}
	if got := EffectivePreference(vm, nil); got != "" {
		t.Error(got)
	}
}

func TestRenderEviction(t *testing.T) {
	in := renderFixture()
	for spec, want := range map[string]string{"LiveMigrate": "LiveMigrate", "": "LiveMigrateIfPossible", "None": "None"} {
		in.VirtualMachine.Spec.EvictionStrategy = spec
		if got, _, _ := unstructured.NestedString(templateSpec(t, mustRender(t, in)), "evictionStrategy"); got != want {
			t.Errorf("%q gave %q", spec, got)
		}
	}
}

func TestRenderFirmwareAndCPU(t *testing.T) {
	spec := templateSpec(t, mustRender(t, renderFixture()))
	secure, _, _ := unstructured.NestedBool(spec, "domain", "firmware", "bootloader", "efi", "secureBoot")
	smm, _, _ := unstructured.NestedBool(spec, "domain", "features", "smm", "enabled")
	tpm, _, _ := unstructured.NestedBool(spec, "domain", "devices", "tpm", "persistent")
	cpu, _, _ := unstructured.NestedMap(spec, "domain", "cpu")
	if !secure || !smm || !tpm || !reflect.DeepEqual(cpu, map[string]any{"dedicatedCpuPlacement": true, "model": "host-model"}) {
		t.Fatalf("domain %+v", spec["domain"])
	}
	in := renderFixture()
	in.VirtualMachine.Spec.Firmware = &v1alpha1.VMFirmware{UEFI: true}
	spec = templateSpec(t, mustRender(t, in))
	secure, found, _ := unstructured.NestedBool(spec, "domain", "firmware", "bootloader", "efi", "secureBoot")
	if _, smmFound, _ := unstructured.NestedMap(spec, "domain", "features", "smm"); secure || !found || smmFound {
		t.Fatalf("domain %+v", spec["domain"])
	}
	in.VirtualMachine.Spec.Firmware, in.VirtualMachine.Spec.CPU = nil, nil
	spec = templateSpec(t, mustRender(t, in))
	for _, path := range [][]string{{"domain", "firmware"}, {"domain", "cpu"}, {"domain", "devices", "tpm"}} {
		if _, found, _ := unstructured.NestedFieldNoCopy(spec, path...); found {
			t.Errorf("%v must be absent", path)
		}
	}
}

func TestRenderHostDevices(t *testing.T) {
	spec := templateSpec(t, mustRender(t, renderFixture()))
	want := []any{map[string]any{"name": "hostdev-0", "deviceName": "nvidia.com/GA102"}}
	if got := list(t, spec, "domain", "devices", "hostDevices"); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestRenderLogsTheSerialConsole(t *testing.T) {
	spec := templateSpec(t, mustRender(t, renderFixture()))
	if logging, _, _ := unstructured.NestedBool(spec, "domain", "devices", "logSerialConsole"); !logging {
		t.Fatal("logSerialConsole must be true")
	}
}

func containsKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for name, inner := range typed {
			if name == key || containsKey(inner, key) {
				return true
			}
		}
	case []any:
		for _, inner := range typed {
			if containsKey(inner, key) {
				return true
			}
		}
	}
	return false
}

func TestRenderNeverSetsNodeName(t *testing.T) {
	if containsKey(mustRender(t, renderFixture()).VirtualMachine.Object, "nodeName") {
		t.Fatal("nodeName must never be set")
	}
}

func TestRenderScheduling(t *testing.T) {
	in := renderFixture()
	before := in.VirtualMachine.DeepCopy()
	spec := templateSpec(t, mustRender(t, in))
	selector, _, _ := unstructured.NestedStringMap(spec, "nodeSelector")
	terms := list(t, spec, "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution")
	if !reflect.DeepEqual(selector, map[string]string{"disk": "ssd"}) || len(terms) != 1 {
		t.Fatalf("spec %+v", spec)
	}
	if !reflect.DeepEqual(&in.VirtualMachine, before) {
		t.Fatal("the input changed")
	}
}

func TestRenderLabelsAndOwner(t *testing.T) {
	in := renderFixture()
	out := mustRender(t, in)
	want := v1alpha1.TenantLabels("VirtualMachine", "web-1", "acme", "web")
	if !reflect.DeepEqual(out.VirtualMachine.GetLabels(), want) || !reflect.DeepEqual(out.RootClaim.GetLabels(), want) || !reflect.DeepEqual(out.CDROMClaims[0].GetLabels(), want) || !reflect.DeepEqual(out.Secret.Labels, want) {
		t.Fatal("labels differ")
	}
	templateLabels, _, _ := unstructured.NestedStringMap(out.VirtualMachine.Object, "spec", "template", "metadata", "labels")
	for key, value := range want {
		if templateLabels[key] != value {
			t.Errorf("template label %s", key)
		}
	}
	refs := out.VirtualMachine.GetOwnerReferences()
	if len(refs) != 1 || !reflect.DeepEqual(refs[0], OwnerRef(in.VirtualMachine)) {
		t.Fatalf("%+v", refs)
	}
}

func TestRenderIsPure(t *testing.T) {
	first := mustRender(t, renderFixture())
	if !reflect.DeepEqual(first, mustRender(t, renderFixture())) {
		t.Fatal("equal input must give equal output")
	}
	if !reflect.DeepEqual(first.VirtualMachine, first.VirtualMachine.DeepCopy()) {
		t.Fatal("the object must hold JSON types only")
	}
}

func TestRenderRefusesNoNetworks(t *testing.T) {
	in := renderFixture()
	in.NICs = nil
	if _, err := Render(in); err == nil {
		t.Fatal("no networks must be refused")
	}
}

func TestRenderCloudInitSecretMatchesUserData(t *testing.T) {
	in := renderFixture()
	out := mustRender(t, in)
	want, err := UserData(in.UserData, in.SSHKeys)
	if err != nil || string(out.Secret.Data["userdata"]) != want || !strings.Contains(want, "ssh-ed25519 AAAAk1") {
		t.Fatalf("%q %v", out.Secret.Data["userdata"], err)
	}
}
