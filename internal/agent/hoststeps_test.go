package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/pkgmgr"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func hostStepEnv(t *testing.T, exec *host.FakeExec, running string) StepEnv {
	t.Helper()
	root := t.TempDir()
	deps := newDeps(exec, time.Now())
	deps.Root = root
	deps.Version = running
	deps.Packages = pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}
	writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/staged/v0.3.0/bedrock"), "new bedrock")
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/bedrock"), "old bedrock")
	sum, err := release.FileSHA256(filepath.Join(root, "var/lib/bedrock/staged/v0.3.0/bedrock"))
	if err != nil {
		t.Fatal(err)
	}
	target := v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.3.0"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.3.0", BedrockChecksums: map[string]string{runtime.GOARCH: sum}}}
	return StepEnv{Deps: deps, Target: target, Upgrade: v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v0.3.0", From: "v0.2.0", Attempt: 1}}}
}

func TestAgentUpdateInstallsTheStagedBinary(t *testing.T) {
	env := hostStepEnv(t, &host.FakeExec{}, "v0.2.0")
	outcome, err := agentUpdate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Restart || outcome.Message != "agent v0.3.0 installed" {
		t.Fatalf("outcome %+v", outcome)
	}
	dest := filepath.Join(env.Deps.Root, "usr/local/bin/bedrock")
	if readFixtureFile(t, dest) != "new bedrock" {
		t.Fatal("the agent binary was not replaced")
	}
	if info, err := os.Stat(dest); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("the agent binary must be executable: %v", err)
	}
}

func TestAgentUpdateSkipsACurrentAgent(t *testing.T) {
	env := hostStepEnv(t, &host.FakeExec{}, "v0.3.0")
	outcome, err := agentUpdate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Restart || outcome.Message != "agent already at v0.3.0" {
		t.Fatalf("outcome %+v", outcome)
	}
	if readFixtureFile(t, filepath.Join(env.Deps.Root, "usr/local/bin/bedrock")) != "old bedrock" {
		t.Fatal("a current agent must not be replaced")
	}
}

func TestAgentUpdateRejectsATamperedBinary(t *testing.T) {
	env := hostStepEnv(t, &host.FakeExec{}, "v0.2.0")
	writeFixtureFile(t, filepath.Join(env.Deps.Root, "var/lib/bedrock/staged/v0.3.0/bedrock"), "tampered")
	if _, err := agentUpdate(context.Background(), env); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error %v", err)
	}
	if readFixtureFile(t, filepath.Join(env.Deps.Root, "usr/local/bin/bedrock")) != "old bedrock" {
		t.Fatal("a tampered binary must not be installed")
	}
}

func TestOSUpdateRunsTheSecurityUpdate(t *testing.T) {
	exec := &host.FakeExec{Responses: map[string]string{"apt-get update": "", "unattended-upgrade -v": ""}}
	outcome, err := osUpdate(context.Background(), hostStepEnv(t, exec, "v0.2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(exec.Calls, []string{"apt-get update", "unattended-upgrade -v"}) || outcome.Message != "security updates applied" {
		t.Fatalf("calls %v outcome %+v", exec.Calls, outcome)
	}
}

func TestRebootOnlyWhenPending(t *testing.T) {
	env := hostStepEnv(t, &host.FakeExec{}, "v0.2.0")
	outcome, err := reboot(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Reboot || outcome.Message != "no reboot pending" {
		t.Fatalf("outcome %+v", outcome)
	}
	writeFixtureFile(t, filepath.Join(env.Deps.Root, "var/run/reboot-required"), "")
	outcome, err = reboot(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Reboot {
		t.Fatalf("a pending reboot must be requested: %+v", outcome)
	}
}

func TestHostStepsAreRegistered(t *testing.T) {
	for _, name := range []string{v1alpha1.StepK0sUpdate, v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate, v1alpha1.StepReboot} {
		if _, ok := Steps()[name]; !ok {
			t.Fatalf("Steps must include %s", name)
		}
	}
}

type k0sHost struct {
	root       string
	versions   map[string]string
	running    string
	startPolls int
	readyPolls int
	starting   int
	restartErr error
	calls      []string
}

func (h *k0sHost) Run(_ context.Context, name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	h.calls = append(h.calls, call)
	switch {
	case strings.HasPrefix(call, "systemctl restart --no-block "):
		if h.restartErr != nil {
			return "", h.restartErr
		}
		raw, err := os.ReadFile(filepath.Join(h.root, "usr/local/bin/k0s"))
		if err != nil {
			return "", err
		}
		h.running, h.starting = h.versions[string(raw)], h.startPolls
		return "", nil
	case call == "/usr/local/bin/k0s status -o json" && h.starting > 0:
		h.starting--
		return "", errors.New("k0s is not running")
	case call == "/usr/local/bin/k0s status -o json" && h.running != "":
		return fmt.Sprintf(`{"Version":%q,"Pid":42}`, h.running), nil
	case call == "/usr/local/bin/k0s status -o json":
		return "", errors.New("k0s is not running")
	case call == readyzCall(h.root) && h.readyPolls > 0:
		h.readyPolls--
		return "", errors.New("connection refused")
	case call == readyzCall(h.root):
		return "ok\n", nil
	}
	return "", fmt.Errorf("no fake response for %q", call)
}

const (
	oldK0s = "v1.36.2+k0s.0"
	newK0s = "v1.36.3+k0s.0"
)

func k0sStepEnv(t *testing.T, node, role string) (StepEnv, *k0sHost) {
	t.Helper()
	h := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: node}, Spec: v1alpha1.HostSpec{Roles: []string{role}}}
	if err := k8sClient.Create(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), h) })
	root := t.TempDir()
	fake := &k0sHost{root: root, versions: map[string]string{"old k0s": oldK0s, "new k0s": newK0s}, running: oldK0s, startPolls: 2}
	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Exec = fake
	deps.Root = root
	deps.Node = node
	deps.K0sTimeout = time.Minute
	deps.K0sPoll = time.Millisecond
	writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/staged/v0.3.0/k0s"), "new k0s")
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s"), "old k0s")
	sum, err := release.FileSHA256(filepath.Join(root, "var/lib/bedrock/staged/v0.3.0/k0s"))
	if err != nil {
		t.Fatal(err)
	}
	target := v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.3.0"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.3.0", K0sVersion: newK0s, K0sChecksums: map[string]string{runtime.GOARCH: sum}}}
	return StepEnv{Deps: deps, Client: k8sClient, Target: target, Upgrade: v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: node, Version: "v0.3.0", From: "v0.2.0", Attempt: 1}}}, fake
}

func readyzCall(root string) string {
	return "/usr/local/bin/k0s kubectl --kubeconfig " + filepath.Join(root, "var/lib/k0s/pki/admin.conf") + " --request-timeout=10s get --raw /readyz"
}

const statusCall = "/usr/local/bin/k0s status -o json"

func TestK0sUpdateSwapsAndWaitsForTheRestartedK0s(t *testing.T) {
	cases := map[string]struct {
		node, role, unit string
		calls            func(root string) []string
	}{
		"controller": {"k0s-controller-a", v1alpha1.RoleControlPlane, "k0scontroller.service", func(root string) []string {
			return []string{"systemctl restart --no-block k0scontroller.service", statusCall, statusCall, statusCall, readyzCall(root), statusCall, readyzCall(root)}
		}},
		"worker": {"k0s-worker-a", v1alpha1.RoleWorkload, "k0sworker.service", func(string) []string {
			return []string{"systemctl restart --no-block k0sworker.service", statusCall, statusCall, statusCall}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, fake := k0sStepEnv(t, tc.node, tc.role)
			fake.readyPolls = 1
			outcome, err := k0sUpdate(context.Background(), env)
			if err != nil {
				t.Fatal(err)
			}
			if outcome != (Outcome{Message: "k0s v1.36.3+k0s.0 installed, restarted " + tc.unit}) {
				t.Fatalf("outcome %+v", outcome)
			}
			if want := tc.calls(env.Deps.Root); !slices.Equal(fake.calls, want) {
				t.Fatalf("calls\n%v\nwant\n%v", fake.calls, want)
			}
			if fake.running != newK0s {
				t.Fatalf("the restart must run the new binary, runs %s", fake.running)
			}
			dest := filepath.Join(env.Deps.Root, "usr/local/bin/k0s")
			if info, err := os.Stat(dest); err != nil || info.Mode().Perm() != 0o755 || readFixtureFile(t, dest) != "new k0s" {
				t.Fatalf("the new k0s binary must be installed and executable: %v", err)
			}
			if _, err := os.Stat(filepath.Join(env.Deps.Root, "var/lib/bedrock/previous/k0s")); !os.IsNotExist(err) {
				t.Fatalf("K0sUpdate must leave previous/k0s to Preload: %v", err)
			}
		})
	}
}

func TestK0sUpdateRestartsAnOldK0sOnTheNewBinary(t *testing.T) {
	env, fake := k0sStepEnv(t, "k0s-resumed-a", v1alpha1.RoleWorkload)
	fake.restartErr = errors.New("systemctl: exit status 1")
	if _, err := k0sUpdate(context.Background(), env); err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("a failed restart must fail the step: %v", err)
	}
	if readFixtureFile(t, filepath.Join(env.Deps.Root, "usr/local/bin/k0s")) != "new k0s" || fake.running != oldK0s {
		t.Fatalf("the file is new and the old k0s still runs: %s", fake.running)
	}
	fake.restartErr, fake.calls = nil, nil
	outcome, err := k0sUpdate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{statusCall, "systemctl restart --no-block k0sworker.service", statusCall, statusCall, statusCall}
	if outcome != (Outcome{Message: "k0s v1.36.3+k0s.0 installed, restarted k0sworker.service"}) || !slices.Equal(fake.calls, want) || fake.running != newK0s {
		t.Fatalf("outcome %+v calls %v running %s", outcome, fake.calls, fake.running)
	}
}

func TestK0sUpdateSkipsARunningTarget(t *testing.T) {
	env, fake := k0sStepEnv(t, "k0s-current-a", v1alpha1.RoleControlPlane)
	writeFixtureFile(t, filepath.Join(env.Deps.Root, "usr/local/bin/k0s"), "new k0s")
	fake.running = newK0s
	outcome, err := k0sUpdate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{statusCall, statusCall, readyzCall(env.Deps.Root)}
	if outcome != (Outcome{Message: "k0s already at v1.36.3+k0s.0"}) || !slices.Equal(fake.calls, want) {
		t.Fatalf("outcome %+v calls %v", outcome, fake.calls)
	}
}

func TestK0sUpdateTimesOut(t *testing.T) {
	cases := map[string]struct {
		node, role string
		change     func(*k0sHost)
		want       string
	}{
		"api never ready":    {"k0s-timeout-a", v1alpha1.RoleControlPlane, func(h *k0sHost) { h.readyPolls = 1 << 30 }, "k0s v1.36.3+k0s.0 is not ready after 50ms: /readyz: connection refused"},
		"k0s never runs":     {"k0s-timeout-b", v1alpha1.RoleWorkload, func(h *k0sHost) { h.startPolls = 1 << 30 }, "k0s v1.36.3+k0s.0 is not ready after 50ms: k0s does not run"},
		"old k0s comes back": {"k0s-timeout-c", v1alpha1.RoleWorkload, func(h *k0sHost) { h.versions["new k0s"] = oldK0s }, "k0s v1.36.3+k0s.0 is not ready after 50ms: k0s runs v1.36.2+k0s.0"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, fake := k0sStepEnv(t, tc.node, tc.role)
			env.Deps.K0sTimeout = 50 * time.Millisecond
			tc.change(fake)
			if _, err := k0sUpdate(context.Background(), env); err == nil || err.Error() != tc.want {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestK0sUpdateRejectsABadStagedBinary(t *testing.T) {
	cases := map[string]struct {
		node   string
		change func(t *testing.T, staged string)
		want   string
	}{
		"missing": {"k0s-missing-a", func(t *testing.T, staged string) {
			if err := os.Remove(staged); err != nil {
				t.Fatal(err)
			}
		}, "no such file or directory"},
		"tampered": {"k0s-tampered-a", func(t *testing.T, staged string) { writeFixtureFile(t, staged, "tampered") }, "checksum"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, fake := k0sStepEnv(t, tc.node, v1alpha1.RoleWorkload)
			tc.change(t, filepath.Join(env.Deps.Root, "var/lib/bedrock/staged/v0.3.0/k0s"))
			_, err := k0sUpdate(context.Background(), env)
			if err == nil || !strings.Contains(err.Error(), "staged/v0.3.0/k0s") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v", err)
			}
			if readFixtureFile(t, filepath.Join(env.Deps.Root, "usr/local/bin/k0s")) != "old k0s" || len(fake.calls) != 0 {
				t.Fatalf("a bad staged binary must change nothing: calls %v", fake.calls)
			}
		})
	}
}
