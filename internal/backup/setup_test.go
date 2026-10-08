package backup

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestSetupRegistersTheControllers(t *testing.T) {
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	ensureProject(t)
	vmb := &v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: "wired", Namespace: projectNamespace}, Spec: v1alpha1.VirtualMachineBackupSpec{VMRef: "wired-vm", Target: "default"}}
	if err := k8sClient.Create(ctx, vmb); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got := &v1alpha1.VirtualMachineBackup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: projectNamespace, Name: "wired"}, got); err == nil && len(got.Finalizers) == 1 && got.Finalizers[0] == BackupFinalizer {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the VirtualMachineBackup controller did not run")
}
