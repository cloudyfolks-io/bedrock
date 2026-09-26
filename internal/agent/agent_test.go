package agent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/hostconfig"
	"github.com/cloudyfolks-labs/bedrock/internal/maintenance"
	"github.com/cloudyfolks-labs/bedrock/internal/pkgmgr"
)

func fakeInventory(ctx context.Context, _ host.Exec, _ string) (v1alpha1.Inventory, error) {
	return v1alpha1.Inventory{CPU: v1alpha1.CPUInfo{Cores: 4}, MemoryBytes: 1 << 30}, nil
}

func newDeps(exec *host.FakeExec, now time.Time) Deps {
	return Deps{Exec: exec, Root: "/nonexistent", Node: "node-a", Now: func() time.Time { return now }, Interval: time.Hour, Inventory: fakeInventory, Apply: hostconfig.Apply, Packages: pkgmgr.Manager{Exec: exec, Family: "apt", Root: "/nonexistent"}, Version: "test", Hostname: func() (string, error) { return "node-a", nil }, DiskSpace: func(string) (host.Space, error) { return host.Space{}, nil }, HTTP: &http.Client{}}
}

func createHost(t *testing.T, name string, managed bool, window string) {
	t.Helper()
	h := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.HostSpec{Roles: []string{"workload"}, Management: v1alpha1.ManagementSpec{Enabled: managed}, MaintenanceWindow: window}}
	if err := k8sClient.Create(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), h) })
}

func getHost(t *testing.T, name string) v1alpha1.Host {
	t.Helper()
	var h v1alpha1.Host
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: name}, &h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTickWritesInventoryOnly(t *testing.T) {
	createHost(t, "node-a", false, "")
	exec := &host.FakeExec{}
	if err := Tick(context.Background(), k8sClient, newDeps(exec, time.Now())); err != nil {
		t.Fatal(err)
	}
	h := getHost(t, "node-a")
	if h.Status.Inventory.CPU.Cores != 4 || h.Status.Inventory.MemoryBytes != 1<<30 {
		t.Fatalf("inventory %+v", h.Status.Inventory)
	}
	if v1alpha1.IsConditionTrue(h.Status.Conditions, v1alpha1.ConditionManagementApplied) {
		t.Fatal("management must not be applied")
	}
	if changes := hostChanges(exec.Calls); len(changes) != 0 {
		t.Fatalf("no host command may change the host when management is off: %v", changes)
	}
}

var readOnlyFacts = []string{"/usr/local/bin/k0s version", "timedatectl show -p NTPSynchronized --value", "/usr/local/bin/k0s etcd member-list"}

func hostChanges(calls []string) []string {
	var changes []string
	for _, call := range calls {
		if !slices.Contains(readOnlyFacts, call) {
			changes = append(changes, call)
		}
	}
	return changes
}

func TestTickReportsHostFacts(t *testing.T) {
	createHost(t, "node-a", false, "")
	exec := &host.FakeExec{Responses: map[string]string{"/usr/local/bin/k0s version": "v1.36.3+k0s.0\n", "timedatectl show -p NTPSynchronized --value": "yes\n"}}
	deps := newDeps(exec, time.Now())
	deps.Version = "v0.3.0"
	deps.Hostname = func() (string, error) { return "Node-A", nil }
	deps.DiskSpace = func(string) (host.Space, error) { return host.Space{FreeBytes: 42 << 30, SizeBytes: 100 << 30}, nil }
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	status := getHost(t, "node-a").Status
	if status.AgentVersion != "v0.3.0" || status.K0sVersion != "v1.36.3+k0s.0" || status.Hostname != "node-a" {
		t.Fatalf("versions and hostname: %+v", status)
	}
	if status.Checks == nil || !status.Checks.TimeSynced || status.Checks.VarLibFreeBytes != 42<<30 || status.Checks.VarLibSizeBytes != 100<<30 {
		t.Fatalf("checks %+v", status.Checks)
	}
}

func createNode(t *testing.T, name, internalIP string) {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8sClient.Create(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), node) })
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeHostName, Address: name}, {Type: corev1.NodeInternalIP, Address: internalIP}}
	if err := k8sClient.Status().Update(context.Background(), node); err != nil {
		t.Fatal(err)
	}
}

func TestNodeInternalIP(t *testing.T) {
	createNode(t, "node-b", "10.0.0.12")
	ip, err := NodeInternalIP(context.Background(), k8sClient, "node-b")
	if err != nil || ip != "10.0.0.12" {
		t.Fatalf("ip %q err %v", ip, err)
	}
	if _, err := NodeInternalIP(context.Background(), k8sClient, "node-missing"); err == nil {
		t.Fatal("a missing node must be an error")
	}
}

func TestTickReportsDepotOnlyWithBundles(t *testing.T) {
	createHost(t, "node-a", false, "")
	createNode(t, "node-a", "10.0.0.11")
	root := t.TempDir()
	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Root = root
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if depot := getHost(t, "node-a").Status.Depot; depot != nil {
		t.Fatalf("an empty depot must not be reported, got %+v", depot)
	}
	manifest := filepath.Join(root, "var/lib/bedrock/depot/v0.3.0/amd64/bundle.yaml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("version: v0.3.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	depot := getHost(t, "node-a").Status.Depot
	if depot == nil || depot.URL != "http://10.0.0.11:9480" || len(depot.Bundles) != 1 || depot.Bundles[0].Version != "v0.3.0" || depot.Bundles[0].Arch != "amd64" {
		t.Fatalf("depot %+v", depot)
	}
}

func TestTickReportsTheRestoreMarker(t *testing.T) {
	createHost(t, "node-a", false, "")
	root := t.TempDir()
	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Root = root
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if restore := getHost(t, "node-a").Status.Restore; restore != nil {
		t.Fatalf("no marker must mean no restore, got %+v", restore)
	}
	marker := `{"backup":"/var/lib/bedrock/backups/a.tar.gz","completedAt":"2026-10-01T10:00:00Z"}`
	if err := os.MkdirAll(filepath.Join(root, "var/lib/bedrock"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "var/lib/bedrock/restore.json"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	got := getHost(t, "node-a").Status.Restore
	if got == nil || got.Backup != "/var/lib/bedrock/backups/a.tar.gz" || !got.CompletedAt.Time.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("restore %+v", got)
	}
}

func TestTickOmitsAppliedWhenUnmanaged(t *testing.T) {
	createHost(t, "node-a", false, "")
	if err := Tick(context.Background(), k8sClient, newDeps(&host.FakeExec{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Host"))
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "node-a"}, live); err != nil {
		t.Fatal(err)
	}
	if applied, found, _ := unstructured.NestedFieldNoCopy(live.Object, "status", "applied"); found {
		t.Fatalf("status.applied must be absent for an unmanaged host, got %v", applied)
	}
}

func TestTickAppliesHostConfigWhenManaged(t *testing.T) {
	createHost(t, "node-a", true, "")
	hc := &v1alpha1.HostConfig{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}, Spec: v1alpha1.HostConfigSpec{Sysctls: map[string]string{"net.ipv4.ip_forward": "1"}}}
	if err := k8sClient.Create(context.Background(), hc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), hc) })
	root := t.TempDir()
	exec := &host.FakeExec{ResponsePrefixes: map[string]string{"sysctl -p ": ""}}
	deps := newDeps(exec, time.Now())
	deps.Root = root
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	h := getHost(t, "node-a")
	if !v1alpha1.IsConditionTrue(h.Status.Conditions, v1alpha1.ConditionManagementApplied) {
		t.Fatalf("conditions %+v", h.Status.Conditions)
	}
	if h.Status.Applied == nil || h.Status.Applied.Generation != hc.Generation || len(h.Status.Applied.Steps) != 7 {
		t.Fatalf("applied %+v", h.Status.Applied)
	}
	calls := len(hostChanges(exec.Calls))
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if len(hostChanges(exec.Calls)) != calls {
		t.Fatalf("second tick at the same generation must not re-apply: %v", hostChanges(exec.Calls))
	}
}

func TestTickReportsMissingHostConfig(t *testing.T) {
	createHost(t, "node-a", true, "")
	if err := Tick(context.Background(), k8sClient, newDeps(&host.FakeExec{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	h := getHost(t, "node-a")
	for _, c := range h.Status.Conditions {
		if c.Type == v1alpha1.ConditionManagementApplied && c.Reason == "NoHostConfig" && c.Status == metav1.ConditionFalse {
			return
		}
	}
	t.Fatalf("conditions %+v", h.Status.Conditions)
}

func TestTickRunsSecurityUpdateInWindow(t *testing.T) {
	createHost(t, "node-a", true, "Sat 02:00-05:00")
	root := t.TempDir()
	exec := &host.FakeExec{Responses: map[string]string{"apt-get update": "", "unattended-upgrade -v": ""}}
	saturday := time.Date(2026, time.September, 12, 3, 0, 0, 0, time.Local)
	deps := newDeps(exec, saturday)
	deps.Root = root
	deps.Packages = pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if changes := hostChanges(exec.Calls); len(changes) != 2 {
		t.Fatalf("calls %v", changes)
	}
	h := getHost(t, "node-a")
	if v1alpha1.IsConditionTrue(h.Status.Conditions, v1alpha1.ConditionRebootPending) {
		t.Fatal("no reboot pending without the marker file")
	}
	pkgmgr.TouchSentinel(root)
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	h = getHost(t, "node-a")
	if !v1alpha1.IsConditionTrue(h.Status.Conditions, v1alpha1.ConditionRebootPending) {
		t.Fatalf("conditions %+v", h.Status.Conditions)
	}
}

func TestTickReportsUpdateFailure(t *testing.T) {
	createHost(t, "node-update", true, "Sat 02:00-05:00")
	root := t.TempDir()
	updateErr := errors.New("apt lock held")
	exec := &host.FakeExec{Responses: map[string]string{"apt-get update": ""}, Errors: map[string]error{"apt-get update": updateErr}}
	saturday := time.Date(2026, time.September, 12, 3, 0, 0, 0, time.Local)
	deps := newDeps(exec, saturday)
	deps.Node = "node-update"
	deps.Root = root
	deps.Packages = pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	cond := requireCondition(t, "node-update", v1alpha1.ConditionRebootPending)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "UpdateFailed" || cond.Message != updateErr.Error() {
		t.Fatalf("condition %+v", cond)
	}
}

func TestTickReportsRebootProbeFailure(t *testing.T) {
	createHost(t, "node-probe", true, "Sat 02:00-05:00")
	root := t.TempDir()
	probeErr := &host.ExitError{Code: 3}
	exec := &host.FakeExec{
		Responses: map[string]string{"dnf upgrade -y --security": "", "needs-restarting -r": ""},
		Errors:    map[string]error{"needs-restarting -r": probeErr},
	}
	saturday := time.Date(2026, time.September, 12, 3, 0, 0, 0, time.Local)
	deps := newDeps(exec, saturday)
	deps.Node = "node-probe"
	deps.Root = root
	deps.Packages = pkgmgr.Manager{Exec: exec, Family: "dnf", Root: root}
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	cond := requireCondition(t, "node-probe", v1alpha1.ConditionRebootPending)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "ProbeFailed" || cond.Message != probeErr.Error() {
		t.Fatalf("condition %+v", cond)
	}
}

func TestTickSkipsUpdateOutsideWindow(t *testing.T) {
	createHost(t, "node-a", true, "Sat 02:00-05:00")
	exec := &host.FakeExec{}
	monday := time.Date(2026, time.September, 14, 3, 0, 0, 0, time.Local)
	if err := Tick(context.Background(), k8sClient, newDeps(exec, monday)); err != nil {
		t.Fatal(err)
	}
	if changes := hostChanges(exec.Calls); len(changes) != 0 {
		t.Fatalf("calls %v", changes)
	}
}

func TestApplyStatusKeepsOtherManagersFields(t *testing.T) {
	createHost(t, "node-a", false, "")
	h := getHost(t, "node-a")
	h.Status.KubernetesVersion = "v1.36.3"
	if err := k8sClient.Status().Update(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
	if err := ApplyStatus(context.Background(), k8sClient, "node-a", v1alpha1.HostStatus{Inventory: v1alpha1.Inventory{MemoryBytes: 42}}); err != nil {
		t.Fatal(err)
	}
	h = getHost(t, "node-a")
	if h.Status.KubernetesVersion != "v1.36.3" || h.Status.Inventory.MemoryBytes != 42 {
		t.Fatalf("status %+v", h.Status)
	}
}

func createHostConfig(t *testing.T, name string, spec v1alpha1.HostConfigSpec) {
	t.Helper()
	hc := &v1alpha1.HostConfig{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
	if err := k8sClient.Create(context.Background(), hc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), hc) })
}

func enableManagement(t *testing.T, name string) {
	t.Helper()
	h := getHost(t, name)
	h.Spec.Management.Enabled = true
	if err := k8sClient.Update(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
}

func requireCondition(t *testing.T, name, conditionType string) metav1.Condition {
	t.Helper()
	h := getHost(t, name)
	cond := apimeta.FindStatusCondition(h.Status.Conditions, conditionType)
	if cond == nil {
		t.Fatalf("no %s condition in %+v", conditionType, h.Status.Conditions)
	}
	return *cond
}

func backdateConditions(t *testing.T, name string, at time.Time) {
	t.Helper()
	h := getHost(t, name)
	for i := range h.Status.Conditions {
		h.Status.Conditions[i].LastTransitionTime = metav1.NewTime(at)
	}
	if err := k8sClient.Status().Update(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
}

func TestTickKeepsTransitionTimeUntilStatusChanges(t *testing.T) {
	createHost(t, "node-stable", false, "")
	exec := &host.FakeExec{ResponsePrefixes: map[string]string{"sysctl -p ": ""}}
	deps := newDeps(exec, time.Now())
	deps.Node = "node-stable"
	deps.Root = t.TempDir()
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	backdated := time.Now().Add(-time.Hour).Truncate(time.Second).UTC()
	backdateConditions(t, "node-stable", backdated)
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	cond := requireCondition(t, "node-stable", v1alpha1.ConditionManagementApplied)
	if cond.Reason != "ManagementDisabled" || !cond.LastTransitionTime.Time.Equal(backdated) {
		t.Fatalf("transition time must survive an unchanged condition: %+v", cond)
	}
	enableManagement(t, "node-stable")
	createHostConfig(t, "node-stable", v1alpha1.HostConfigSpec{})
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	cond = requireCondition(t, "node-stable", v1alpha1.ConditionManagementApplied)
	if cond.Status != metav1.ConditionTrue || cond.LastTransitionTime.Time.Equal(backdated) {
		t.Fatalf("a status flip must move the transition time: %+v", cond)
	}
}

func TestTickKeepsRebootPendingOutsideWindow(t *testing.T) {
	createHost(t, "node-carry", true, "Sat 02:00-05:00")
	root := t.TempDir()
	exec := &host.FakeExec{Responses: map[string]string{"apt-get update": "", "unattended-upgrade -v": ""}}
	deps := newDeps(exec, time.Date(2026, time.September, 12, 3, 0, 0, 0, time.Local))
	deps.Node = "node-carry"
	deps.Root = root
	deps.Packages = pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}
	if err := pkgmgr.TouchSentinel(root); err != nil {
		t.Fatal(err)
	}
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	cond := requireCondition(t, "node-carry", v1alpha1.ConditionRebootPending)
	if cond.Status != metav1.ConditionTrue || cond.Reason != "SecurityUpdate" {
		t.Fatalf("condition %+v", cond)
	}
	calls := len(hostChanges(exec.Calls))
	monday := time.Date(2026, time.September, 14, 3, 0, 0, 0, time.Local)
	deps.Now = func() time.Time { return monday }
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if len(hostChanges(exec.Calls)) != calls {
		t.Fatalf("a closed window must run nothing: %v", hostChanges(exec.Calls))
	}
	cond = requireCondition(t, "node-carry", v1alpha1.ConditionRebootPending)
	if cond.Status != metav1.ConditionTrue || cond.Reason != "SecurityUpdate" {
		t.Fatalf("a pending reboot must survive a closed window: %+v", cond)
	}
}

func TestTickReportsInvalidWindow(t *testing.T) {
	window := "Someday 02:00-05:00"
	createHost(t, "node-badwindow", true, window)
	exec := &host.FakeExec{}
	deps := newDeps(exec, time.Now())
	deps.Node = "node-badwindow"
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if changes := hostChanges(exec.Calls); len(changes) != 0 {
		t.Fatalf("calls %v", changes)
	}
	_, parseErr := maintenance.Parse(window)
	if parseErr == nil {
		t.Fatal("the window must not parse")
	}
	cond := requireCondition(t, "node-badwindow", v1alpha1.ConditionRebootPending)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "InvalidWindow" || cond.Message != parseErr.Error() {
		t.Fatalf("condition %+v", cond)
	}
}

func TestTickReportsFirstFailedStep(t *testing.T) {
	createHost(t, "node-failing", true, "")
	createHostConfig(t, "node-failing", v1alpha1.HostConfigSpec{Modules: []string{"dummy"}})
	exec := &host.FakeExec{
		Responses:        map[string]string{"modprobe dummy": ""},
		ResponsePrefixes: map[string]string{"sysctl -p ": ""},
		Errors:           map[string]error{"modprobe dummy": errors.New("module not found")},
	}
	deps := newDeps(exec, time.Now())
	deps.Node = "node-failing"
	deps.Root = t.TempDir()
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	cond := requireCondition(t, "node-failing", v1alpha1.ConditionManagementApplied)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "StepFailed" || cond.Message != "modules: dummy: module not found" {
		t.Fatalf("condition %+v", cond)
	}
}

func TestTickReturnsInventoryErrorAndStillWritesStatus(t *testing.T) {
	createHost(t, "node-noinv", false, "")
	gather := errors.New("gather failed")
	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Node = "node-noinv"
	deps.Inventory = func(context.Context, host.Exec, string) (v1alpha1.Inventory, error) {
		return v1alpha1.Inventory{}, gather
	}
	if err := Tick(context.Background(), k8sClient, deps); !errors.Is(err, gather) {
		t.Fatalf("err %v", err)
	}
	cond := requireCondition(t, "node-noinv", v1alpha1.ConditionManagementApplied)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "ManagementDisabled" {
		t.Fatalf("condition %+v", cond)
	}
}

type flakyWatchClient struct {
	client.WithWatch
	failures  *atomic.Int32
	successes *atomic.Int32
}

func (f flakyWatchClient) Watch(ctx context.Context, list client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
	if f.failures.Add(-1) >= 0 {
		return nil, errors.New("no matching kind")
	}
	w, err := f.WithWatch.Watch(ctx, list, opts...)
	if err == nil {
		f.successes.Add(1)
	}
	return w, err
}

func TestRunRetriesTheWatchUntilItSucceeds(t *testing.T) {
	createHost(t, "node-a", false, "")
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	var failures, successes atomic.Int32
	failures.Store(2)
	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Interval = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, flakyWatchClient{WithWatch: watching, failures: &failures, successes: &successes}, deps)
	}()
	deadline := time.After(5 * time.Second)
	for successes.Load() < 2 {
		select {
		case err := <-done:
			t.Fatalf("Run returned before the watch succeeded: %v", err)
		case <-deadline:
			t.Fatalf("watch never succeeded, successes %d", successes.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type closedResultWatch struct{}

func (closedResultWatch) Stop() {}

func (closedResultWatch) ResultChan() <-chan watch.Event {
	events := make(chan watch.Event)
	close(events)
	return events
}

type closingWatchClient struct {
	client.WithWatch
	calls *atomic.Int32
}

func (c closingWatchClient) Watch(context.Context, client.ObjectList, ...client.ListOption) (watch.Interface, error) {
	c.calls.Add(1)
	return closedResultWatch{}, nil
}

func TestRunWaitsATickBeforeRewatching(t *testing.T) {
	createHost(t, "node-a", false, "")
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Interval = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, closingWatchClient{WithWatch: watching, calls: &calls}, deps) }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := calls.Load()
	if got < 2 {
		t.Fatalf("watch calls %d, the fake watch was never used", got)
	}
	if got > 36 {
		t.Fatalf("watch calls %d, a closed watch must wait a tick before the next attempt", got)
	}
}

func TestRunWakesForOwnNodeUpgrades(t *testing.T) {
	createHost(t, "node-a", false, "")
	createReleases(t)
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Root = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, watching, deps) }()
	time.Sleep(500 * time.Millisecond)
	createUpgrade(t, "node-a", v1alpha1.StepPreload)
	deadline := time.Now().Add(5 * time.Second)
	for len(getUpgrade(t, "node-a").Status.Steps) == 0 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("a new NodeUpgrade must wake the loop although the interval is an hour")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
