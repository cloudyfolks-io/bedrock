package compute

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func existingShapes() map[string]RenderInput {
	full := renderFixture()

	plain := renderFixture()
	plain.VirtualMachine.Spec.CDROMs = nil
	plain.VirtualMachine.Spec.Firmware = nil
	plain.VirtualMachine.Spec.CPU = nil
	plain.VirtualMachine.Spec.HostDevices = nil
	plain.VirtualMachine.Spec.Scheduling = nil
	plain.VirtualMachine.Spec.EvictionStrategy = ""
	plain.CDROMs = nil

	blank := plain
	blank.VirtualMachine = fixtureVM("acme-web", "web-2")
	blank.VirtualMachine.UID = types.UID("vm-uid-2")
	blank.VirtualMachine.Spec.Image = nil
	blank.VirtualMachine.Spec.Preference = ""
	blank.VirtualMachine.Spec.Disks = nil
	blank.VirtualMachine.Spec.Running = false
	blank.VirtualMachine.Spec.RootDisk = v1alpha1.VMRootDisk{SizeGi: 20, StorageClass: "block"}
	blank.RootImage, blank.ImageOS, blank.Volumes = nil, nil, nil

	windows := plain
	windows.VirtualMachine.Spec.Firmware = &v1alpha1.VMFirmware{UEFI: true}
	windows.VirtualMachine.Spec.Preference = "windows.2k22"
	windows.NetworkData = "version: 2\n"

	return map[string]RenderInput{"full": full, "plain": plain, "blank": blank, "windows": windows}
}

func renderedJSON(t *testing.T, in RenderInput) []byte {
	t.Helper()
	out, err := json.MarshalIndent(mustRender(t, in), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func TestRenderOfExistingShapesIsStable(t *testing.T) {
	for name, in := range existingShapes() {
		want, err := os.ReadFile(filepath.Join("testdata", "render-"+name+".golden.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got := renderedJSON(t, in); string(got) != string(want) {
			t.Errorf("the render of the %s shape changed:\n%s", name, got)
		}
	}
}
