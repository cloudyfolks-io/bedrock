package backup

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func snapshotInstanceType(obj *unstructured.Unstructured) string {
	var snapshot struct {
		Spec struct {
			InstanceType string `json:"instanceType"`
		} `json:"spec"`
	}
	_ = json.Unmarshal([]byte(obj.GetAnnotations()[VMSnapshotAnnotation]), &snapshot)
	return snapshot.Spec.InstanceType
}

func waitUntilQuiet(t *testing.T, ctx context.Context, vmb string) {
	t.Helper()
	const steady = 10
	last, unchanged := "", 0
	err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) {
		current := &v1alpha1.VirtualMachineBackup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: projectNamespace, Name: vmb}, current); err != nil {
			return false, nil
		}
		if len(current.Status.Conditions) == 0 {
			return false, nil
		}
		if current.ResourceVersion == last {
			unchanged++
		} else {
			last, unchanged = current.ResourceVersion, 0
		}
		return unchanged >= steady, nil
	})
	if err != nil {
		t.Fatalf("the backup %s never settled: %v", vmb, err)
	}
}

func TestVirtualMachineChangeQueuesItsBackups(t *testing.T) {
	ensureProject(t)
	for name, vm := range map[string]string{"watch-a": "watch-vm", "watch-b": "watch-vm", "watch-other": "other-vm"} {
		createVMB(t, name, v1alpha1.VirtualMachineBackupSpec{VMRef: vm})
	}
	r := newBackupReconciler(testNow)
	vm := &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "watch-vm", Namespace: projectNamespace}}
	var got []string
	for _, request := range r.backupsOfVM(context.Background(), vm) {
		if request.Namespace != projectNamespace {
			t.Fatalf("request %v", request)
		}
		got = append(got, request.Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"watch-a", "watch-b"}) {
		t.Fatalf("queued %v", got)
	}
}

func TestVirtualMachineChangeRefreshesTheScheduleThroughTheWatch(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "live-vm")
	createVMB(t, "live", v1alpha1.VirtualMachineBackupSpec{VMRef: "live-vm", Schedule: "0 2 * * *"})
	createFixtureBackup(t, "live-20261008", "live", "Completed", "2026-10-08T02:05:00Z")
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &BackupReconciler{Client: mgr.GetClient(), Now: time.Now, Recorder: record.NewFakeRecorder(100)}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	waitForInstanceType := func(want string) {
		err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) {
			schedule, err := getVelero(t, "Schedule", VeleroName("vmb", projectNamespace, "live"))
			return err == nil && snapshotInstanceType(schedule) == want, nil
		})
		if err != nil {
			t.Fatalf("the schedule never carried the instance type %q: %v", want, err)
		}
	}
	waitForInstanceType("small")
	waitUntilQuiet(t, ctx, "live")
	updateVM(t, "live-vm", func(vm *v1alpha1.VirtualMachine) { vm.Spec.InstanceType = "large" })
	waitForInstanceType("large")
}
