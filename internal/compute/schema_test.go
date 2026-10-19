package compute

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var schemaSeq atomic.Int64

func schemaName(prefix string) string {
	return prefix + "-" + strconv.FormatInt(schemaSeq.Add(1), 10)
}

func requireAccepted(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	if err := c.Create(context.Background(), obj); err != nil {
		t.Fatalf("create %s: %v", obj.GetName(), err)
	}
}

func requireRefused(t *testing.T, c client.Client, label string, obj client.Object) {
	t.Helper()
	err := c.Create(context.Background(), obj)
	if err == nil {
		t.Errorf("%s: create was accepted", label)
		return
	}
	if !apierrors.IsInvalid(err) {
		t.Errorf("%s: want an Invalid error, got %v", label, err)
	}
}

func schemaVM(ns string, mutate func(*v1alpha1.VirtualMachine)) *v1alpha1.VirtualMachine {
	vm := fixtureVM(ns, schemaName("vm"))
	mutate(&vm)
	return &vm
}

func schemaVolume(ns string, mutate func(*v1alpha1.Volume)) *v1alpha1.Volume {
	volume := v1alpha1.Volume{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: schemaName("vol")},
		Spec:       v1alpha1.VolumeSpec{SizeGi: 10},
	}
	mutate(&volume)
	return &volume
}

func TestSchemaRefusesForeignReferences(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	longName := strings.Repeat("a", 64)
	vmCases := map[string]func(*v1alpha1.VirtualMachine){
		"subnet slash":   func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SubnetRef = "other/default" },
		"subnet comma":   func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SubnetRef = "a,b" },
		"subnet json":    func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SubnetRef = `{"x":1}` },
		"subnet upper":   func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SubnetRef = "Default" },
		"subnet long":    func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SubnetRef = longName },
		"security group": func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SecurityGroups = []string{"a/b"} },
		"elastic ip":     func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].ElasticIPRef = "x/y" },
		"volume":         func(vm *v1alpha1.VirtualMachine) { vm.Spec.Disks[0].VolumeRef = "a/b" },
		"ssh key":        func(vm *v1alpha1.VirtualMachine) { vm.Spec.SSHKeys = []string{"a b"} },
		"cdrom":          func(vm *v1alpha1.VirtualMachine) { vm.Spec.CDROMs = []v1alpha1.VMCDROM{{ImageRef: "a/b"}} },
		"secret":         func(vm *v1alpha1.VirtualMachine) { vm.Spec.CloudInit = &v1alpha1.VMCloudInit{SecretRef: "a/b"} },
		"image":          func(vm *v1alpha1.VirtualMachine) { vm.Spec.Image.Name = "a/b" },
	}
	for label, mutate := range vmCases {
		requireRefused(t, c, label, schemaVM(ns, mutate))
	}
	volumeCases := map[string]func(*v1alpha1.Volume){
		"snapshot": func(v *v1alpha1.Volume) { v.Spec.Source = &v1alpha1.VolumeDataSource{SnapshotRef: "a/b"} },
		"volume":   func(v *v1alpha1.Volume) { v.Spec.Source = &v1alpha1.VolumeDataSource{VolumeRef: "a/b"} },
		"image": func(v *v1alpha1.Volume) {
			v.Spec.Source = &v1alpha1.VolumeDataSource{Image: &v1alpha1.ImageRef{Name: "a/b"}}
		},
	}
	for label, mutate := range volumeCases {
		requireRefused(t, c, "volume source "+label, schemaVolume(ns, mutate))
	}
	requireRefused(t, c, "snapshot volume", &v1alpha1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: schemaName("snap")},
		Spec:       v1alpha1.VolumeSnapshotSpec{VolumeRef: "a/b"},
	})
	requireAccepted(t, c, schemaVM(ns, func(*v1alpha1.VirtualMachine) {}))
	requireAccepted(t, c, schemaVolume(ns, func(v *v1alpha1.Volume) {
		v.Spec.Source = &v1alpha1.VolumeDataSource{Image: &v1alpha1.ImageRef{Name: "ubuntu-24.04"}}
	}))
}

func TestSchemaRefusesDuplicateAndOversizedLists(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	names := func(prefix string, count int) []string {
		out := make([]string, 0, count)
		for i := 0; i < count; i++ {
			out = append(out, prefix+strconv.Itoa(i))
		}
		return out
	}
	cases := map[string]func(*v1alpha1.VirtualMachine){
		"duplicate subnet": func(vm *v1alpha1.VirtualMachine) {
			vm.Spec.Networks = []v1alpha1.VMNetwork{{SubnetRef: "a"}, {SubnetRef: "a"}}
		},
		"duplicate volume": func(vm *v1alpha1.VirtualMachine) {
			vm.Spec.Disks = []v1alpha1.VMDisk{{VolumeRef: "a"}, {VolumeRef: "a"}}
		},
		"nine networks": func(vm *v1alpha1.VirtualMachine) {
			vm.Spec.Networks = nil
			for _, n := range names("n", 9) {
				vm.Spec.Networks = append(vm.Spec.Networks, v1alpha1.VMNetwork{SubnetRef: n})
			}
		},
		"25 disks": func(vm *v1alpha1.VirtualMachine) {
			vm.Spec.Disks = nil
			for _, n := range names("d", 25) {
				vm.Spec.Disks = append(vm.Spec.Disks, v1alpha1.VMDisk{VolumeRef: n})
			}
		},
		"five cdroms": func(vm *v1alpha1.VirtualMachine) {
			vm.Spec.CDROMs = nil
			for _, n := range names("c", 5) {
				vm.Spec.CDROMs = append(vm.Spec.CDROMs, v1alpha1.VMCDROM{ImageRef: n})
			}
		},
		"17 ssh keys": func(vm *v1alpha1.VirtualMachine) { vm.Spec.SSHKeys = names("k", 17) },
		"nine host devices": func(vm *v1alpha1.VirtualMachine) {
			vm.Spec.HostDevices = nil
			for _, n := range names("h", 9) {
				vm.Spec.HostDevices = append(vm.Spec.HostDevices, v1alpha1.VMHostDevice{ClaimRef: n})
			}
		},
		"nine security groups": func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SecurityGroups = names("g", 9) },
	}
	for label, mutate := range cases {
		requireRefused(t, c, label, schemaVM(ns, mutate))
	}
	long := fixtureVM(ns, strings.Repeat("a", 53))
	requireRefused(t, c, "53 character name", &long)
	dotted := fixtureVM(ns, "web.one")
	requireRefused(t, c, "dotted name", &dotted)
	ok := fixtureVM(ns, strings.Repeat("a", 52))
	requireAccepted(t, c, &ok)
}

func TestSchemaRefusesCloudInitMix(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	cases := map[string]*v1alpha1.VMCloudInit{
		"secret and user data":    {SecretRef: "s", UserData: "x"},
		"secret and network data": {SecretRef: "s", NetworkData: "x"},
		"oversized user data":     {UserData: strings.Repeat("a", 65537)},
	}
	for label, ci := range cases {
		requireRefused(t, c, label, schemaVM(ns, func(vm *v1alpha1.VirtualMachine) { vm.Spec.CloudInit = ci }))
	}
	requireAccepted(t, c, schemaVM(ns, func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.CloudInit = &v1alpha1.VMCloudInit{UserData: "a", NetworkData: "b"}
	}))
}

func requireUpdateRefused(t *testing.T, c client.Client, label string, obj client.Object) {
	t.Helper()
	err := c.Update(context.Background(), obj)
	if err == nil {
		t.Errorf("%s: update was accepted", label)
		return
	}
	if !apierrors.IsInvalid(err) {
		t.Errorf("%s: want an Invalid error, got %v", label, err)
	}
}

func TestSchemaRefusesVolumeShrink(t *testing.T) {
	c, _ := startEnv(t)
	ctx := context.Background()
	ns := uniqueNamespace(t, c)
	volume := schemaVolume(ns, func(v *v1alpha1.Volume) { v.Spec.StorageClass = "block" })
	requireAccepted(t, c, volume)
	volume.Spec.SizeGi = 20
	if err := c.Update(ctx, volume); err != nil {
		t.Fatalf("grow: %v", err)
	}
	volume.Spec.SizeGi = 5
	requireUpdateRefused(t, c, "shrink", volume)
	if err := c.Get(ctx, client.ObjectKeyFromObject(volume), volume); err != nil {
		t.Fatal(err)
	}
	volume.Spec.StorageClass = "filesystem"
	requireUpdateRefused(t, c, "storage class", volume)

	snapshot := &v1alpha1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: schemaName("snap")},
		Spec:       v1alpha1.VolumeSnapshotSpec{VolumeRef: "a", Retention: &metav1.Duration{Duration: time.Hour}},
	}
	requireAccepted(t, c, snapshot)
	snapshot.Spec.VolumeRef = "b"
	requireUpdateRefused(t, c, "snapshot volume", snapshot)

	vm := schemaVM(ns, func(vm *v1alpha1.VirtualMachine) { vm.Spec.RootDisk.StorageClass = "block" })
	requireAccepted(t, c, vm)
	vm.Spec.RootDisk.SizeGi = 80
	if err := c.Update(ctx, vm); err != nil {
		t.Fatalf("root grow: %v", err)
	}
	vm.Spec.RootDisk.SizeGi = 20
	requireUpdateRefused(t, c, "root shrink", vm)
	if err := c.Get(ctx, client.ObjectKeyFromObject(vm), vm); err != nil {
		t.Fatal(err)
	}
	vm.Spec.RootDisk.StorageClass = "filesystem"
	requireUpdateRefused(t, c, "root storage class", vm)
}

func TestSchemaSSHKeyIsOneLine(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	for _, key := range []string{"ssh-ed25519 AAAA\nssh-ed25519 BBBB", "ssh-ed25519 AAAA\r"} {
		requireRefused(t, c, "multi line key", &v1alpha1.SSHKey{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: schemaName("key")},
			Spec:       v1alpha1.SSHKeySpec{PublicKey: key},
		})
	}
	requireAccepted(t, c, &v1alpha1.SSHKey{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: schemaName("key")},
		Spec:       v1alpha1.SSHKeySpec{PublicKey: "ssh-ed25519 AAAA alice"},
	})
}

func TestSchemaDiskBusHasNoDefault(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	vm := schemaVM(ns, func(*v1alpha1.VirtualMachine) {})
	requireAccepted(t, c, vm)
	var read v1alpha1.VirtualMachine
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(vm), &read); err != nil {
		t.Fatal(err)
	}
	if read.Spec.Disks[0].Bus != "" {
		t.Fatalf("disk bus = %q, want empty", read.Spec.Disks[0].Bus)
	}
}

func TestSchemaRefusesBadCPUModel(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	requireRefused(t, c, "cpu model with space", schemaVM(ns, func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.CPU = &v1alpha1.VMCPU{Model: "host model"}
	}))
	requireAccepted(t, c, schemaVM(ns, func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.CPU = &v1alpha1.VMCPU{Model: "host-model"}
	}))
}

func TestSchemaRefusesBadNICAddress(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	cases := map[string]string{
		"hostname":     "web",
		"ipv6":         "fd00::5",
		"ipv4 mapped":  "::ffff:10.0.0.5",
		"out of range": "10.0.0.256",
		"prefix":       "10.0.0.5/24",
		"leading zero": "010.0.0.5",
		"padded":       " 10.0.0.5",
	}
	for label, address := range cases {
		requireRefused(t, c, label, schemaVM(ns, func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].Address = address }))
	}
	requireAccepted(t, c, schemaVM(ns, func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].Address = "10.100.0.50" }))
}

func TestSchemaNICAddressIsImmutable(t *testing.T) {
	c, _ := startEnv(t)
	ctx := context.Background()
	ns := uniqueNamespace(t, c)
	vm := schemaVM(ns, func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Networks = []v1alpha1.VMNetwork{{SubnetRef: "default", Address: "10.100.0.50"}, {SubnetRef: "backend"}, {SubnetRef: "data", Address: "10.102.0.7"}}
	})
	requireAccepted(t, c, vm)
	vm.Spec.Running = false
	if err := c.Update(ctx, vm); err != nil {
		t.Fatalf("an update that keeps the addresses: %v", err)
	}
	mutations := map[string]func(*v1alpha1.VirtualMachine){
		"change": func(v *v1alpha1.VirtualMachine) { v.Spec.Networks[0].Address = "10.100.0.51" },
		"remove": func(v *v1alpha1.VirtualMachine) { v.Spec.Networks[0].Address = "" },
		"add":    func(v *v1alpha1.VirtualMachine) { v.Spec.Networks[1].Address = "10.101.0.9" },
		"swap": func(v *v1alpha1.VirtualMachine) {
			v.Spec.Networks[0].Address, v.Spec.Networks[2].Address = v.Spec.Networks[2].Address, v.Spec.Networks[0].Address
		},
	}
	for label, mutate := range mutations {
		changed := vm.DeepCopy()
		mutate(changed)
		requireUpdateRefused(t, c, label, changed)
	}
	reordered := vm.DeepCopy()
	reordered.Spec.Networks[0], reordered.Spec.Networks[2] = reordered.Spec.Networks[2], reordered.Spec.Networks[0]
	if err := c.Update(ctx, reordered); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	reordered.Spec.Networks = reordered.Spec.Networks[:2]
	if err := c.Update(ctx, reordered); err != nil {
		t.Fatalf("detach a network: %v", err)
	}
	reordered.Spec.Networks = append(reordered.Spec.Networks, v1alpha1.VMNetwork{SubnetRef: "default", Address: "10.100.0.77"})
	if err := c.Update(ctx, reordered); err != nil {
		t.Fatalf("attach the network again with a new address: %v", err)
	}
}

func TestSchemaRefusesBadGuestIdentity(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	cases := map[string]func(*v1alpha1.VirtualMachine){
		"uuid text": func(vm *v1alpha1.VirtualMachine) { vm.Spec.Firmware = &v1alpha1.VMFirmware{UUID: "web-1"} },
		"uuid short": func(vm *v1alpha1.VirtualMachine) {
			vm.Spec.Firmware = &v1alpha1.VMFirmware{UUID: "6f1f3a52-2f0b-4c1e-9d55"}
		},
		"serial space": func(vm *v1alpha1.VirtualMachine) { vm.Spec.Firmware = &v1alpha1.VMFirmware{Serial: "a b"} },
		"serial long": func(vm *v1alpha1.VirtualMachine) {
			vm.Spec.Firmware = &v1alpha1.VMFirmware{Serial: strings.Repeat("a", 65)}
		},
		"disk serial": func(vm *v1alpha1.VirtualMachine) { vm.Spec.Disks[0].Serial = "a b" },
		"disk long":   func(vm *v1alpha1.VirtualMachine) { vm.Spec.Disks[0].Serial = strings.Repeat("a", 37) },
	}
	for label, mutate := range cases {
		requireRefused(t, c, label, schemaVM(ns, mutate))
	}
	requireAccepted(t, c, schemaVM(ns, func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Firmware = &v1alpha1.VMFirmware{UUID: "6f1f3a52-2f0b-4c1e-9d55-0c6f1d7a9e10", Serial: "SN-0001"}
		vm.Spec.Disks[0].Serial = "vol0123456789abcdef"
	}))
}

func TestSchemaFirmwareIdentityIsImmutable(t *testing.T) {
	c, _ := startEnv(t)
	ctx := context.Background()
	ns := uniqueNamespace(t, c)
	identified := schemaVM(ns, func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Firmware = &v1alpha1.VMFirmware{UEFI: true, UUID: "6f1f3a52-2f0b-4c1e-9d55-0c6f1d7a9e10", Serial: "SN-0001"}
	})
	requireAccepted(t, c, identified)
	identified.Spec.Running = false
	identified.Spec.Firmware.TPM = true
	if err := c.Update(ctx, identified); err != nil {
		t.Fatalf("an update that keeps the identity: %v", err)
	}
	for label, mutate := range map[string]func(*v1alpha1.VirtualMachine){
		"uuid change":   func(v *v1alpha1.VirtualMachine) { v.Spec.Firmware.UUID = "7f1f3a52-2f0b-4c1e-9d55-0c6f1d7a9e10" },
		"uuid remove":   func(v *v1alpha1.VirtualMachine) { v.Spec.Firmware.UUID = "" },
		"serial change": func(v *v1alpha1.VirtualMachine) { v.Spec.Firmware.Serial = "SN-0002" },
		"serial remove": func(v *v1alpha1.VirtualMachine) { v.Spec.Firmware.Serial = "" },
		"firmware drop": func(v *v1alpha1.VirtualMachine) { v.Spec.Firmware = nil },
	} {
		changed := identified.DeepCopy()
		mutate(changed)
		requireUpdateRefused(t, c, label, changed)
	}
	plain := schemaVM(ns, func(*v1alpha1.VirtualMachine) {})
	requireAccepted(t, c, plain)
	for label, firmware := range map[string]*v1alpha1.VMFirmware{
		"uuid add":   {UUID: "6f1f3a52-2f0b-4c1e-9d55-0c6f1d7a9e10"},
		"serial add": {Serial: "SN-0001"},
	} {
		changed := plain.DeepCopy()
		changed.Spec.Firmware = firmware
		requireUpdateRefused(t, c, label, changed)
	}
	plain.Spec.Firmware = &v1alpha1.VMFirmware{UEFI: true}
	if err := c.Update(ctx, plain); err != nil {
		t.Fatalf("firmware without identity: %v", err)
	}
}
