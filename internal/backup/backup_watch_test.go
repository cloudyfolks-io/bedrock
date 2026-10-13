package backup

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if schedule, err := getVelero(t, "Schedule", VeleroName("vmb", projectNamespace, "live")); err == nil {
				if snapshotInstanceType(schedule) == want {
					return
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("the schedule never carried the instance type %q", want)
	}
	waitForInstanceType("small")
	time.Sleep(3 * time.Second)
	updateVM(t, "live-vm", func(vm *v1alpha1.VirtualMachine) { vm.Spec.InstanceType = "large" })
	waitForInstanceType("large")
}
