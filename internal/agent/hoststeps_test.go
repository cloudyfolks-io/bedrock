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
	for _, name := range []string{v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate, v1alpha1.StepReboot} {
		if _, ok := Steps()[name]; !ok {
			t.Fatalf("Steps must include %s", name)
		}
	}
}
