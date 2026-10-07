package operator

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func validVirtualMachine(name string) *v1alpha1.VirtualMachine {
	return &v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.VirtualMachineSpec{
			InstanceType:     "u1.medium",
			Preference:       "ubuntu",
			Image:            &v1alpha1.ImageRef{Name: "ubuntu-24.04", Scope: v1alpha1.ImageScopeCluster},
			RootDisk:         v1alpha1.VMRootDisk{SizeGi: 40},
			Disks:            []v1alpha1.VMDisk{{VolumeRef: "data", Bus: v1alpha1.DiskBusVirtio}},
			Networks:         []v1alpha1.VMNetwork{{SubnetRef: "default", SecurityGroups: []string{"web"}, MACAddress: "02:00:00:aa:bb:cc"}},
			CloudInit:        &v1alpha1.VMCloudInit{UserData: "#cloud-config"},
			SSHKeys:          []string{"alice"},
			Running:          true,
			EvictionStrategy: v1alpha1.EvictionStrategyLiveMigrate,
			Firmware:         &v1alpha1.VMFirmware{UEFI: true, SecureBoot: true, TPM: true},
		},
	}
}

func TestVirtualMachineValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	vm := validVirtualMachine("web")
	createValid(t, c, vm)

	vm.Status.Phase = v1alpha1.VMPhaseRunning
	vm.Status.Addresses = []v1alpha1.VMAddress{{Subnet: "default", IP: "10.100.0.5"}}
	if err := c.Status().Update(ctx, vm); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	vm.Status.Phase = "Paused"
	requireInvalid(t, c.Status().Update(ctx, vm), "Unsupported value")

	stopped := validVirtualMachine("stopped")
	stopped.Spec.Running = false
	createValid(t, c, stopped)
	var fresh v1alpha1.VirtualMachine
	if err := c.Get(ctx, client.ObjectKeyFromObject(stopped), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Spec.Running {
		t.Fatal("running false must persist")
	}

	badEviction := validVirtualMachine("bad-eviction")
	badEviction.Spec.EvictionStrategy = "Always"
	createInvalid(t, c, badEviction, "Unsupported value")

	badBus := validVirtualMachine("bad-bus")
	badBus.Spec.Disks[0].Bus = "ide"
	createInvalid(t, c, badBus, "Unsupported value")

	badMAC := validVirtualMachine("bad-mac")
	badMAC.Spec.Networks[0].MACAddress = "not-a-mac"
	createInvalid(t, c, badMAC, "macAddress")

	secureBootWithoutUEFI := validVirtualMachine("bad-firmware")
	secureBootWithoutUEFI.Spec.Firmware = &v1alpha1.VMFirmware{SecureBoot: true}
	createInvalid(t, c, secureBootWithoutUEFI, "secureBoot requires uefi")

	noRootDisk := validVirtualMachine("no-root-disk")
	noRootDisk.Spec.RootDisk.SizeGi = 0
	createInvalid(t, c, noRootDisk, "rootDisk.sizeGi")
}

func TestVolumeKindsValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	volume := &v1alpha1.Volume{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default"},
		Spec:       v1alpha1.VolumeSpec{SizeGi: 20, StorageClass: "ceph-block", Source: &v1alpha1.VolumeDataSource{Image: &v1alpha1.ImageRef{Name: "ubuntu-24.04"}}},
	}
	createValid(t, c, volume)
	if volume.Spec.Bus != v1alpha1.DiskBusVirtio {
		t.Fatalf("bus must default to virtio: %q", volume.Spec.Bus)
	}

	volume.Status.Phase = v1alpha1.VolumePhaseInUse
	volume.Status.AttachedTo = "web"
	volume.Status.ActualSizeGi = 20
	if err := c.Status().Update(ctx, volume); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	volume.Spec.SizeGi = 40
	if err := c.Update(ctx, volume); err != nil {
		t.Fatalf("a volume must expand in place: %v", err)
	}
	volume.Spec.SizeGi = 10
	updateInvalid(t, c, volume, "sizeGi can only grow")

	var fresh v1alpha1.Volume
	if err := c.Get(ctx, client.ObjectKeyFromObject(volume), &fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Spec.StorageClass = "ceph-fs"
	updateInvalid(t, c, &fresh, "storageClass is immutable")

	twoSources := &v1alpha1.Volume{
		ObjectMeta: metav1.ObjectMeta{Name: "two-sources", Namespace: "default"},
		Spec:       v1alpha1.VolumeSpec{SizeGi: 5, Source: &v1alpha1.VolumeDataSource{SnapshotRef: "snap", VolumeRef: "data"}},
	}
	createInvalid(t, c, twoSources, "at most one source must be set")

	badBus := &v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Name: "bad-bus", Namespace: "default"}, Spec: v1alpha1.VolumeSpec{SizeGi: 5, Bus: "ide"}}
	createInvalid(t, c, badBus, "Unsupported value")

	snapshot := &v1alpha1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "data-1", Namespace: "default"},
		Spec:       v1alpha1.VolumeSnapshotSpec{VolumeRef: "data", Retention: &metav1.Duration{Duration: 720 * time.Hour}},
	}
	createValid(t, c, snapshot)
	snapshot.Status.Phase = v1alpha1.VolumeSnapshotPhaseReady
	if err := c.Status().Update(ctx, snapshot); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	createInvalid(t, c, &v1alpha1.VolumeSnapshot{ObjectMeta: metav1.ObjectMeta{Name: "no-volume", Namespace: "default"}}, "spec.volumeRef")

	key := &v1alpha1.SSHKey{ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "default"}, Spec: v1alpha1.SSHKeySpec{PublicKey: "ssh-ed25519 AAAAC3Nza alice@example"}}
	createValid(t, c, key)
	createInvalid(t, c, &v1alpha1.SSHKey{ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "default"}}, "spec.publicKey")
}
