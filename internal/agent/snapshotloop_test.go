package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

type blockingBackup struct {
	*host.FakeExec
	started *atomic.Bool
	stopped *atomic.Bool
}

func (b blockingBackup) Run(ctx context.Context, name string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "backup" {
		b.started.Store(true)
		<-ctx.Done()
		b.stopped.Store(true)
		return "", context.Cause(ctx)
	}
	return b.FakeExec.Run(ctx, name, args...)
}

func TestRunKeepsReconcilingWhileASnapshotIsRunning(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	var started, stopped atomic.Bool
	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Root = t.TempDir()
	deps.Exec = blockingBackup{FakeExec: &host.FakeExec{}, started: &started, stopped: &stopped}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, watching, deps) }()
	stop := sync.OnceValue(func() error {
		cancel()
		return <-done
	})
	t.Cleanup(func() { _ = stop() })
	deadline := time.Now().Add(10 * time.Second)
	for !started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the snapshot never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	enableManagement(t, "node-a")
	want := getHost(t, "node-a").Generation
	for requireCondition(t, "node-a", v1alpha1.ConditionManagementApplied).ObservedGeneration != want {
		if time.Now().After(deadline) {
			t.Fatal("the loop did not reconcile while the snapshot ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if !stopped.Load() {
		t.Fatal("Run returned before the snapshot stopped")
	}
}
