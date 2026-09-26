package agent

import (
	"context"
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

func k0sStepEnv(t *testing.T, exec *host.FakeExec, node, role string) StepEnv {
	t.Helper()
	h := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: node}, Spec: v1alpha1.HostSpec{Roles: []string{role}}}
	if err := k8sClient.Create(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), h) })
	root := t.TempDir()
	deps := newDeps(exec, time.Now())
	deps.Root = root
	deps.Node = node
	writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/staged/v0.3.0/k0s"), "new k0s")
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s"), "old k0s")
	sum, err := release.FileSHA256(filepath.Join(root, "var/lib/bedrock/staged/v0.3.0/k0s"))
	if err != nil {
		t.Fatal(err)
	}
	target := v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.3.0"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.3.0", K0sVersion: "v1.36.3+k0s.0", K0sChecksums: map[string]string{runtime.GOARCH: sum}}}
	return StepEnv{Deps: deps, Client: k8sClient, Target: target, Upgrade: v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: node, Version: "v0.3.0", From: "v0.2.0", Attempt: 1}}}
}

func TestK0sUpdateSwapsTheBinaryAndRestartsK0s(t *testing.T) {
	cases := map[string]struct {
		node, role, unit string
		bare             bool
	}{
		"controller":    {"k0s-controller-a", v1alpha1.RoleControlPlane, "k0scontroller.service", false},
		"worker":        {"k0s-worker-a", v1alpha1.RoleWorkload, "k0sworker.service", false},
		"bare checksum": {"k0s-worker-b", v1alpha1.RoleCephOSD, "k0sworker.service", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			restart := "systemctl restart --no-block " + tc.unit
			exec := &host.FakeExec{Responses: map[string]string{restart: ""}}
			env := k0sStepEnv(t, exec, tc.node, tc.role)
			if tc.bare {
				env.Target.Spec.K0sChecksums[runtime.GOARCH] = strings.TrimPrefix(env.Target.Spec.K0sChecksums[runtime.GOARCH], "sha256:")
			}
			outcome, err := k0sUpdate(context.Background(), env)
			if err != nil {
				t.Fatal(err)
			}
			if outcome != (Outcome{Message: "k0s v1.36.3+k0s.0 installed, restarting " + tc.unit}) {
				t.Fatalf("outcome %+v", outcome)
			}
			if !slices.Equal(exec.Calls, []string{restart}) {
				t.Fatalf("calls %v", exec.Calls)
			}
			dest := filepath.Join(env.Deps.Root, "usr/local/bin/k0s")
			if readFixtureFile(t, dest) != "new k0s" {
				t.Fatal("the k0s binary was not replaced")
			}
			if info, err := os.Stat(dest); err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("the k0s binary must be executable: %v", err)
			}
			if _, err := os.Stat(filepath.Join(env.Deps.Root, "var/lib/bedrock/previous/k0s")); !os.IsNotExist(err) {
				t.Fatalf("K0sUpdate must leave previous/k0s to Preload: %v", err)
			}
		})
	}
}

func TestK0sUpdateSkipsACurrentBinary(t *testing.T) {
	exec := &host.FakeExec{}
	env := k0sStepEnv(t, exec, "k0s-current-a", v1alpha1.RoleControlPlane)
	writeFixtureFile(t, filepath.Join(env.Deps.Root, "usr/local/bin/k0s"), "new k0s")
	outcome, err := k0sUpdate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != (Outcome{Message: "k0s already at v1.36.3+k0s.0"}) || len(exec.Calls) != 0 {
		t.Fatalf("outcome %+v calls %v", outcome, exec.Calls)
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
			exec := &host.FakeExec{}
			env := k0sStepEnv(t, exec, tc.node, v1alpha1.RoleWorkload)
			tc.change(t, filepath.Join(env.Deps.Root, "var/lib/bedrock/staged/v0.3.0/k0s"))
			_, err := k0sUpdate(context.Background(), env)
			if err == nil || !strings.Contains(err.Error(), "staged k0s") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v", err)
			}
			if readFixtureFile(t, filepath.Join(env.Deps.Root, "usr/local/bin/k0s")) != "old k0s" || len(exec.Calls) != 0 {
				t.Fatalf("a bad staged binary must change nothing: calls %v", exec.Calls)
			}
		})
	}
}
