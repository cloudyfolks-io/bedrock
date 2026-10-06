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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
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
	deps Deps
	exec *host.FakeExec
	node string
}

func apiserverCmdline(t *testing.T, root string, args ...string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(root, "proc", "4242", "cmdline"), strings.Join(append([]string{"kube-apiserver"}, args...), "\x00")+"\x00")
}

func newAuthnArgsFixture(t *testing.T, node, role string) authnArgsFixture {
	t.Helper()
	h := hostWithRole(node, role)
	h.Annotations = map[string]string{v1alpha1.AnnotationAuthnRestart: time.Now().UTC().Format(time.RFC3339)}
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
	touch(t, filepath.Join(root, "etc/bedrock/authn/webhook.kubeconfig"), old)
	fakeAPIServer(t, root, time.Now().Add(-time.Hour))
	apiserverCmdline(t, root, "--etcd-servers=https://127.0.0.1:2379")
	return authnArgsFixture{deps: deps, exec: exec, node: node}
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

func (f authnArgsFixture) setGrant(t *testing.T, value *string) {
	t.Helper()
	var current v1alpha1.Host
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: f.node}, &current); err != nil {
		t.Fatal(err)
	}
	current.Annotations = map[string]string{}
	if value != nil {
		current.Annotations[v1alpha1.AnnotationAuthnRestart] = *value
	}
	if err := k8sClient.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
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

func TestEnableAuthnArgsWaitsForTheOperatorGrant(t *testing.T) {
	stale := time.Now().Add(-v1alpha1.AuthnRestartGrantLifetime - time.Minute).UTC().Format(time.RFC3339)
	cases := map[string]*string{
		"no grant":         nil,
		"stale grant":      &stale,
		"unparsable grant": ptr.To("soon"),
	}
	for name, grant := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAuthnArgsFixture(t, "cp-args-"+strings.ReplaceAll(name, " ", "-"), v1alpha1.RoleControlPlane)
			f.setGrant(t, grant)
			deps := f.restartsWithFlags(t)
			if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
				t.Fatal(err)
			}
			if f.restarts() != 0 {
				t.Fatalf("a controller restarts only on a fresh grant: %v", f.exec.Calls)
			}
			fresh := time.Now().UTC().Format(time.RFC3339)
			f.setGrant(t, &fresh)
			if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
				t.Fatal(err)
			}
			if f.restarts() != 1 {
				t.Fatalf("a fresh grant allows the restart: %v", f.exec.Calls)
			}
		})
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

func controllerService() k0sService {
	return k0sServiceOf(hostWithRole("any", v1alpha1.RoleControlPlane))
}

func (f authnArgsFixture) runsWithFlagAndConfig(t *testing.T) {
	t.Helper()
	withArgs, _, err := withAuthnArgs([]byte(plainConfig))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.deps.Root, "etc/k0s/k0s.yaml")
	writeFixtureFile(t, path, string(withArgs))
	touch(t, path, time.Now().Add(-2*time.Hour))
	apiserverCmdline(t, f.deps.Root, "--authentication-config=/etc/bedrock/authn/authentication.yaml")
}

func TestAuthnRestartWanted(t *testing.T) {
	cases := map[string]struct {
		change func(t *testing.T, f authnArgsFixture)
		want   bool
	}{
		"first enablement: flag missing and config newer": {func(t *testing.T, f authnArgsFixture) {
			touch(t, filepath.Join(f.deps.Root, "etc/k0s/k0s.yaml"), time.Now())
		}, true},
		"flag missing and config older": {func(*testing.T, authnArgsFixture) {}, false},
		"flag present and webhook file newer": {func(t *testing.T, f authnArgsFixture) {
			f.runsWithFlagAndConfig(t)
			touch(t, filepath.Join(f.deps.Root, "etc/bedrock/authn/webhook.kubeconfig"), time.Now())
		}, true},
		"flag present and nothing newer": {func(t *testing.T, f authnArgsFixture) {
			f.runsWithFlagAndConfig(t)
			touch(t, filepath.Join(f.deps.Root, "etc/bedrock/authn/webhook.kubeconfig"), time.Now().Add(-2*time.Hour))
		}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAuthnArgsFixture(t, "cp-wanted-"+strings.ReplaceAll(strings.SplitN(name, ":", 2)[0], " ", "-"), v1alpha1.RoleControlPlane)
			tc.change(t, f)
			if got := authnRestartWanted(f.deps.Root, controllerService()); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	f := newAuthnArgsFixture(t, "wk-wanted", v1alpha1.RoleWorkload)
	touch(t, filepath.Join(f.deps.Root, "etc/bedrock/authn/webhook.kubeconfig"), time.Now())
	if authnRestartWanted(f.deps.Root, k0sServiceOf(hostWithRole("wk", v1alpha1.RoleWorkload))) {
		t.Fatal("a worker never restarts for authn")
	}
}

func TestEnableAuthnArgsRestartsForAChangedWebhook(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-args-webhook", v1alpha1.RoleControlPlane)
	f.runsWithFlagAndConfig(t)
	touch(t, filepath.Join(f.deps.Root, "etc/bedrock/authn/webhook.kubeconfig"), time.Now())
	deps := f.restartsWithFlags(t)
	f.setGrant(t, nil)
	if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if f.restarts() != 0 {
		t.Fatalf("no grant, no restart: %v", f.exec.Calls)
	}
	fresh := time.Now().UTC().Format(time.RFC3339)
	f.setGrant(t, &fresh)
	if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if f.restarts() != 1 {
		t.Fatalf("a changed webhook file restarts with a grant: %v", f.exec.Calls)
	}
	if err := EnableAuthnArgs(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if f.restarts() != 1 {
		t.Fatalf("a kube-apiserver started after the webhook file restarts no more: %v", f.exec.Calls)
	}
}

func TestAuthnRestartProblemWaitsForTheWebhookFile(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-problem-webhook", v1alpha1.RoleControlPlane)
	f.runsWithFlagAndConfig(t)
	webhook := filepath.Join(f.deps.Root, "etc/bedrock/authn/webhook.kubeconfig")
	touch(t, webhook, time.Now())
	if got := authnRestartProblem(context.Background(), f.deps); !strings.Contains(got, "webhook.kubeconfig") {
		t.Fatalf("a kube-apiserver older than the webhook file has not restarted: %q", got)
	}
	touch(t, webhook, time.Now().Add(-2*time.Hour))
	if got := authnRestartProblem(context.Background(), f.deps); got != "" {
		t.Fatalf("a kube-apiserver newer than every file has restarted: %q", got)
	}
}
