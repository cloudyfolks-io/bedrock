package addons

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type countingClient struct {
	client.Client
	gets *atomic.Int64
	kind string
}

func (c countingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, isAddon := obj.(*v1alpha1.Addon); isAddon && c.kind == "Addon" {
		c.gets.Add(1)
	}
	if _, isHost := obj.(*v1alpha1.Host); isHost && c.kind == "Host" {
		c.gets.Add(1)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func startManager(t *testing.T, setup func(ctrl.Manager) error) {
	t.Helper()
	mgr, err := ctrl.NewManager(testConfig, ctrl.Options{Scheme: testScheme, Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: ptr.To(true)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := setup(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mgr.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func settled(count *atomic.Int64) int64 {
	last := count.Load()
	for stable := 0; stable < 6; {
		time.Sleep(250 * time.Millisecond)
		if now := count.Load(); now != last {
			last, stable = now, 0
			continue
		}
		stable++
	}
	return last
}

func increases(count *atomic.Int64, before int64) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if count.Load() > before {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func TestAddonControllerReactsOnlyToRelevantHostAndSettingChanges(t *testing.T) {
	c := newClient(t)
	resetEnv(t, c)
	t.Cleanup(func() { resetEnv(t, c) })
	var gets atomic.Int64
	addHost(t, c, "w1", v1alpha1.RoleWorkload, boolPtr(false), true)
	setSetting(t, c, "images.keep", "3")
	setSetting(t, c, "loki.retention-days", "14")
	createAddon(t, c, "kata", true, "")
	startManager(t, func(mgr ctrl.Manager) error {
		r := &Reconciler{Client: countingClient{Client: mgr.GetClient(), gets: &gets, kind: "Addon"}, Bundle: testBundle, Interval: time.Hour, ReadyInterval: time.Hour}
		return r.SetupWithManager(mgr)
	})
	base := settled(&gets)
	if base == 0 {
		t.Fatal("the controller never reconciled")
	}
	updateHostStatus(t, c, "w1", func(host *v1alpha1.Host) { host.Status.Checks.VarLibFreeBytes = 42 })
	updateSettingValue(t, c, "images.keep", "4")
	if after := settled(&gets); after != base {
		t.Fatalf("an irrelevant Host or Setting change caused %d reconciles", after-base)
	}
	updateSettingValue(t, c, "loki.retention-days", "2")
	if !increases(&gets, base) {
		t.Fatal("a retention change did not reconcile")
	}
	base = settled(&gets)
	updateHostStatus(t, c, "w1", func(host *v1alpha1.Host) { host.Status.Checks.KVM = boolPtr(true) })
	if !increases(&gets, base) {
		t.Fatal("a kvm change did not reconcile")
	}
}

func TestKVMLabelerReactsOnlyToKVMAndLabelChanges(t *testing.T) {
	c := newClient(t)
	resetEnv(t, c)
	t.Cleanup(func() { resetEnv(t, c) })
	var gets atomic.Int64
	createHostAndNode(t, c, "wiring-node", boolPtr(false), nil)
	startManager(t, func(mgr ctrl.Manager) error {
		return (&KVMLabeler{Client: countingClient{Client: mgr.GetClient(), gets: &gets, kind: "Host"}}).SetupWithManager(mgr)
	})
	base := settled(&gets)
	if base == 0 {
		t.Fatal("the controller never reconciled")
	}
	updateHostStatus(t, c, "wiring-node", func(host *v1alpha1.Host) { host.Status.Checks.VarLibFreeBytes = 42 })
	var node corev1.Node
	if err := c.Get(context.Background(), client.ObjectKey{Name: "wiring-node"}, &node); err != nil {
		t.Fatal(err)
	}
	node.Labels = map[string]string{"unrelated": "1"}
	if err := c.Update(context.Background(), &node); err != nil {
		t.Fatal(err)
	}
	if after := settled(&gets); after != base {
		t.Fatalf("an irrelevant Host or Node change caused %d reconciles", after-base)
	}
	updateHostStatus(t, c, "wiring-node", func(host *v1alpha1.Host) { host.Status.Checks.KVM = boolPtr(true) })
	if !increases(&gets, base) {
		t.Fatal("a kvm change did not reconcile")
	}
}

func updateHostStatus(t *testing.T, c client.Client, name string, change func(*v1alpha1.Host)) {
	t.Helper()
	var host v1alpha1.Host
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &host); err != nil {
		t.Fatal(err)
	}
	change(&host)
	if err := c.Status().Update(context.Background(), &host); err != nil {
		t.Fatal(err)
	}
}

func updateSettingValue(t *testing.T, c client.Client, name, value string) {
	t.Helper()
	var setting v1alpha1.Setting
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &setting); err != nil {
		t.Fatal(err)
	}
	setting.Spec.Value = value
	if err := c.Update(context.Background(), &setting); err != nil {
		t.Fatal(err)
	}
}
