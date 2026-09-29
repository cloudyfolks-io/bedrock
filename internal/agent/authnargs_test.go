package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	restartCall = "systemctl restart --no-block k0scontroller.service"
	plainConfig = "apiVersion: k0s.k0sproject.io/v1beta1\nkind: ClusterConfig\nspec:\n  api:\n    extraArgs:\n      default-not-ready-toleration-seconds: \"30\"\n"
)

type restartHost struct {
	*host.FakeExec
	onRestart func()
}

func (h restartHost) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := h.FakeExec.Run(ctx, name, args...)
	if err == nil && strings.TrimSpace(name+" "+strings.Join(args, " ")) == restartCall {
		h.onRestart()
	}
	return out, err
}

type authnArgsFixture struct {
	deps  Deps
	exec  *host.FakeExec
	lease client.ObjectKey
}

func apiserverCmdline(t *testing.T, root string, args ...string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(root, "proc", "4242", "cmdline"), strings.Join(append([]string{"kube-apiserver"}, args...), "\x00")+"\x00")
}

func restartLease(t *testing.T) client.ObjectKey {
	t.Helper()
	ctx := context.Background()
	if err := client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}})); err != nil {
		t.Fatal(err)
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: authnRestartLease}}
	if err := client.IgnoreAlreadyExists(k8sClient.Create(ctx, lease)); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKeyFromObject(lease)
	t.Cleanup(func() {
		var current coordinationv1.Lease
		if err := k8sClient.Get(context.Background(), key, &current); err == nil {
			current.Spec = coordinationv1.LeaseSpec{}
			_ = k8sClient.Update(context.Background(), &current)
		}
	})
	return key
}

func newAuthnArgsFixture(t *testing.T, node, role string) authnArgsFixture {
	t.Helper()
	h := hostWithRole(node, role)
	if err := k8sClient.Create(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), &h) })
	exec := &host.FakeExec{Responses: map[string]string{restartCall: ""}}
	deps := authnDeps(t, time.Now())
	deps.Node = node
	deps.Exec = exec
	deps.K0sTimeout = 300 * time.Millisecond
	deps.K0sPoll = 10 * time.Millisecond
	deps.ProbeTimeout = time.Second
	root := deps.Root
	exec.Responses[readyzCall(root)] = "ok"
	writeFixtureFile(t, filepath.Join(root, "etc/k0s/k0s.yaml"), plainConfig)
	old := time.Now().Add(-2 * time.Hour)
	touch(t, filepath.Join(root, "etc/k0s/k0s.yaml"), old)
	writeFixtureFile(t, filepath.Join(root, "etc/bedrock/authn/authentication.yaml"), "a")
	writeFixtureFile(t, filepath.Join(root, "etc/bedrock/authn/webhook.kubeconfig"), "w")
	fakeAPIServer(t, root, time.Now().Add(-time.Hour))
	apiserverCmdline(t, root, "--etcd-servers=https://127.0.0.1:2379")
	return authnArgsFixture{deps: deps, exec: exec, lease: restartLease(t)}
}

func touch(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func (f authnArgsFixture) restartsWithFlags(t *testing.T) Deps {
	t.Helper()
	deps := f.deps
	deps.Exec = restartHost{FakeExec: f.exec, onRestart: func() {
		fakeAPIServer(t, deps.Root, time.Now().Add(time.Minute))
		apiserverCmdline(t, deps.Root, "--authentication-config=/etc/bedrock/authn/authentication.yaml")
	}}
	return deps
}

func (f authnArgsFixture) restarts() int {
	count := 0
	for _, call := range f.exec.Calls {
		if call == restartCall {
			count++
		}
	}
	return count
}

func holder(t *testing.T, key client.ObjectKey) string {
	t.Helper()
	var lease coordinationv1.Lease
	if err := k8sClient.Get(context.Background(), key, &lease); err != nil {
		t.Fatal(err)
	}
	return ptr.Deref(lease.Spec.HolderIdentity, "")
}

func TestEnableAuthnArgsRestartsTheControllerOnce(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-args-once", v1alpha1.RoleControlPlane)
	deps := f.restartsWithFlags(t)
	if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	args := extraArgsOf(t, []byte(readFixtureFile(t, filepath.Join(deps.Root, "etc/k0s/k0s.yaml"))))
	if args["authentication-config"] != "/etc/bedrock/authn/authentication.yaml" || args["default-not-ready-toleration-seconds"] != "30" {
		t.Fatalf("extraArgs %v", args)
	}
	if f.restarts() != 1 || !slices.Contains(f.exec.Calls, readyzCall(deps.Root)) {
		t.Fatalf("one restart and a readyz wait: %v", f.exec.Calls)
	}
	if got := holder(t, f.lease); got != "" {
		t.Fatalf("the lease must be released, held by %q", got)
	}
	if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if f.restarts() != 1 {
		t.Fatalf("a kube-apiserver with the flags must not restart again: %v", f.exec.Calls)
	}
}

func TestEnableAuthnArgsNeverLoopsRestarts(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-args-loop", v1alpha1.RoleControlPlane)
	deps := f.deps
	deps.Exec = restartHost{FakeExec: f.exec, onRestart: func() { fakeAPIServer(t, deps.Root, time.Now().Add(time.Minute)) }}
	if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
			t.Fatal(err)
		}
	}
	if f.restarts() != 1 {
		t.Fatalf("a kube-apiserver started after its config restarts no more, even without the flags: %v", f.exec.Calls)
	}
}

func TestEnableAuthnArgsRetriesAFailedRestartOnALaterTick(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-args-retry", v1alpha1.RoleControlPlane)
	f.exec.Errors = map[string]error{restartCall: errors.New("unit busy")}
	if err := EnableAuthnArgs(context.Background(), k8sClient, f.deps); err == nil || !strings.Contains(err.Error(), "unit busy") {
		t.Fatalf("error %v", err)
	}
	if got := holder(t, f.lease); got != "" {
		t.Fatalf("a failed restart must release the lease, held by %q", got)
	}
	delete(f.exec.Errors, restartCall)
	if err := EnableAuthnArgs(context.Background(), k8sClient, f.deps); err == nil || !strings.Contains(err.Error(), "kube-apiserver did not restart within 300ms") {
		t.Fatalf("a restart that never brings a new kube-apiserver times out: %v", err)
	}
	deps := f.restartsWithFlags(t)
	if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if f.restarts() != 3 {
		t.Fatalf("each later tick tries again until a new kube-apiserver runs: %v", f.exec.Calls)
	}
}

func TestEnableAuthnArgsWaitsForTheLease(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-args-lease", v1alpha1.RoleControlPlane)
	ctx := context.Background()
	var lease coordinationv1.Lease
	if err := k8sClient.Get(ctx, f.lease, &lease); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewMicroTime(time.Now())
	lease.Spec = coordinationv1.LeaseSpec{HolderIdentity: ptr.To("cp-other"), RenewTime: &now, LeaseDurationSeconds: ptr.To(int32(900))}
	if err := k8sClient.Update(ctx, &lease); err != nil {
		t.Fatal(err)
	}
	deps := f.restartsWithFlags(t)
	if err := EnableAuthnArgs(ctx, k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if f.restarts() != 0 || holder(t, f.lease) != "cp-other" {
		t.Fatalf("another controller holds the lease: %v", f.exec.Calls)
	}
	if err := k8sClient.Get(ctx, f.lease, &lease); err != nil {
		t.Fatal(err)
	}
	stale := metav1.NewMicroTime(time.Now().Add(-time.Hour))
	lease.Spec.RenewTime = &stale
	if err := k8sClient.Update(ctx, &lease); err != nil {
		t.Fatal(err)
	}
	if err := EnableAuthnArgs(ctx, k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if f.restarts() != 1 || holder(t, f.lease) != "" {
		t.Fatalf("an expired lease is taken over: %v", f.exec.Calls)
	}
}

func TestEnableAuthnArgsWaitsForTheNodeUpgrade(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-args-upgrade", v1alpha1.RoleControlPlane)
	ctx := context.Background()
	upgrade := &v1alpha1.NodeUpgrade{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v2", "cp-args-upgrade")}, Spec: v1alpha1.NodeUpgradeSpec{Node: "cp-args-upgrade", Version: "v2", From: "v1", Attempt: 1, Steps: []string{v1alpha1.StepK0sUpdate}}}
	if err := k8sClient.Create(ctx, upgrade); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), upgrade) })
	if err := EnableAuthnArgs(ctx, k8sClient, f.restartsWithFlags(t)); err != nil {
		t.Fatal(err)
	}
	if f.restarts() != 0 || readFixtureFile(t, filepath.Join(f.deps.Root, "etc/k0s/k0s.yaml")) != plainConfig {
		t.Fatalf("the K0sUpdate step owns the node during an upgrade: %v", f.exec.Calls)
	}
}

func TestEnableAuthnArgsLeavesOtherHosts(t *testing.T) {
	cases := map[string]struct {
		node, role string
		change     func(t *testing.T, f authnArgsFixture)
	}{
		"worker": {"wk-args", v1alpha1.RoleWorkload, func(*testing.T, authnArgsFixture) {}},
		"no authn files": {"cp-args-nofiles", v1alpha1.RoleControlPlane, func(t *testing.T, f authnArgsFixture) {
			if err := os.Remove(filepath.Join(f.deps.Root, "etc/bedrock/authn/webhook.kubeconfig")); err != nil {
				t.Fatal(err)
			}
		}},
		"flags already running": {"cp-args-running", v1alpha1.RoleControlPlane, func(t *testing.T, f authnArgsFixture) {
			apiserverCmdline(t, f.deps.Root, "--authentication-config=/etc/bedrock/authn/authentication.yaml")
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAuthnArgsFixture(t, tc.node, tc.role)
			tc.change(t, f)
			if err := EnableAuthnArgs(context.Background(), k8sClient, f.restartsWithFlags(t)); err != nil {
				t.Fatal(err)
			}
			if f.restarts() != 0 {
				t.Fatalf("no restart: %v", f.exec.Calls)
			}
		})
	}
}
