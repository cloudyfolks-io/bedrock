package compute

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func createMountingContainer(t *testing.T, c client.Client, namespace, name, volume string) {
	t.Helper()
	createObject(t, c, &v1alpha1.Container{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: v1alpha1.ContainerSpec{
			Image:   "registry.test/web:1",
			Volumes: []v1alpha1.ContainerVolume{{VolumeRef: volume, MountPath: "/data"}},
			Network: v1alpha1.ContainerNetwork{SubnetRef: "default"},
		},
	})
}

func removeContainerVolumes(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	var container v1alpha1.Container
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &container))
	container.Spec.Volumes = nil
	must(t, c.Update(context.Background(), &container))
}

func TestVolumeAttachedToAContainer(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	reconcileVolume(t, c, ns, "data1")
	setDataVolumePhase(t, c, ns, "data1", "Succeeded")
	createMountingContainer(t, c, ns, "web", "data1")
	volume := reconcileVolume(t, c, ns, "data1")
	if volume.Status.AttachedTo != "Container/web" || volume.Status.Phase != v1alpha1.VolumePhaseInUse {
		t.Fatalf("status %+v", volume.Status)
	}
	removeContainerVolumes(t, c, ns, "web")
	volume = reconcileVolume(t, c, ns, "data1")
	if volume.Status.AttachedTo != "" || volume.Status.Phase != v1alpha1.VolumePhaseAvailable {
		t.Fatalf("a freed volume has no holder: %+v", volume.Status)
	}
}

func TestVolumeDeleteWaitsForTheContainerAndKeepsTheData(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	reconcileVolume(t, c, ns, "data1")
	createMountingContainer(t, c, ns, "web", "data1")
	must(t, c.Delete(context.Background(), &v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "data1"}}))
	volume := reconcileVolume(t, c, ns, "data1")
	if volume.DeletionTimestamp.IsZero() || !containsString(volume.Finalizers, FinalizerVolumeInUse) {
		t.Fatalf("volume %+v", volume.ObjectMeta)
	}
	requireVolumeReason(t, volume, "InUse")
	if message := volumeReady(volume).Message; !strings.Contains(message, `the container "web"`) {
		t.Fatalf("message %q", message)
	}
	must(t, c.Delete(context.Background(), &v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web"}}))
	reconcileVolume(t, c, ns, "data1")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &v1alpha1.Volume{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the volume must be gone once the container is gone: %v", err)
	}
}

func TestVMRefusesAVolumeHeldByAnOlderContainer(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createMountingContainer(t, c, ns, "web", "data1")
	time.Sleep(1100 * time.Millisecond)
	createVM(t, c, ns, "late", noMutation)
	reconcileVM(t, newVMReconciler(c), ns, "late")
	requireReason(t, getVM(t, c, ns, "late"), "VolumeInUse")
	requireAbsent(t, c, kubeVirtVMGVK, ns, "late")
	if message := readyOf(getVM(t, c, ns, "late")).Message; !strings.Contains(message, "Container/web") {
		t.Fatalf("message %q", message)
	}
}

func TestVMKeepsAVolumeWhenItIsOlderThanTheContainer(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "early", noMutation)
	time.Sleep(1100 * time.Millisecond)
	createMountingContainer(t, c, ns, "web", "data1")
	reconcileVM(t, newVMReconciler(c), ns, "early")
	requirePresent(t, c, kubeVirtVMGVK, ns, "early")
}

func TestVolumeConsumerNamesTheHolder(t *testing.T) {
	at := func(minute int) metav1.ObjectMeta {
		return metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(fixedNow.Add(time.Duration(minute) * time.Minute))}
	}
	vm := v1alpha1.VirtualMachine{ObjectMeta: at(2), Spec: v1alpha1.VirtualMachineSpec{Disks: []v1alpha1.VMDisk{{VolumeRef: "d"}}}}
	vm.Name = "vm"
	older := v1alpha1.Container{ObjectMeta: at(1), Spec: v1alpha1.ContainerSpec{Volumes: []v1alpha1.ContainerVolume{{VolumeRef: "d", MountPath: "/d"}}}}
	older.Name = "older"
	if got := VolumeConsumer([]v1alpha1.VirtualMachine{vm}, []v1alpha1.Container{older}, "d"); got != "Container/older" {
		t.Fatalf("holder %q", got)
	}
	if got := VolumeConsumer([]v1alpha1.VirtualMachine{vm}, nil, "d"); got != "vm" {
		t.Fatalf("holder %q", got)
	}
	if got := VolumeConsumer(nil, nil, "d"); got != "" {
		t.Fatalf("holder %q", got)
	}
}
