package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/audit"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
)

func makeLogDir(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, audit.LogDir), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureAuditArgsNeedsThePolicyAndTheLogDirectory(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "etc/k0s/k0s.yaml")
	writeFixtureFile(t, config, plainConfig)
	service := controllerService()
	changed, err := ensureAuditArgs(root, service)
	if err != nil || changed || readFixtureFile(t, config) != plainConfig {
		t.Fatalf("no policy and no directory: %v %v", changed, err)
	}
	writeFixtureFile(t, policyPath(root), "policy")
	changed, err = ensureAuditArgs(root, service)
	if err != nil || changed || readFixtureFile(t, config) != plainConfig {
		t.Fatalf("a policy without the log directory must not add flags: %v %v", changed, err)
	}
	if err := os.Remove(policyPath(root)); err != nil {
		t.Fatal(err)
	}
	makeLogDir(t, root)
	changed, err = ensureAuditArgs(root, service)
	if err != nil || changed || readFixtureFile(t, config) != plainConfig {
		t.Fatalf("a log directory without the policy must not add flags: %v %v", changed, err)
	}
	writeFixtureFile(t, policyPath(root), "policy")
	changed, err = ensureAuditArgs(root, service)
	if err != nil || !changed {
		t.Fatalf("both present must add the flags: %v %v", changed, err)
	}
	args := extraArgsOf(t, []byte(readFixtureFile(t, config)))
	for name, value := range audit.Args() {
		if args[name] != value {
			t.Errorf("%s = %v, want %s", name, args[name], value)
		}
	}
}

func TestEnsureAuditArgsLeavesWorkers(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, "etc/k0s/k0s.yaml"), plainConfig)
	writeFixtureFile(t, policyPath(root), "policy")
	makeLogDir(t, root)
	changed, err := ensureAuditArgs(root, k0sServiceOf(hostWithRole("w", v1alpha1.RoleWorkload)))
	if err != nil || changed {
		t.Fatalf("a worker has no kube-apiserver: %v %v", changed, err)
	}
}

func TestEnsureAuditArgsKeepsUserFlags(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "etc/k0s/k0s.yaml")
	writeFixtureFile(t, config, "apiVersion: k0s.k0sproject.io/v1beta1\nkind: ClusterConfig\nspec:\n  api:\n    extraArgs:\n      audit-log-maxsize: \"50\"\n")
	writeFixtureFile(t, policyPath(root), "policy")
	makeLogDir(t, root)
	if _, err := ensureAuditArgs(root, controllerService()); err != nil {
		t.Fatal(err)
	}
	args := extraArgsOf(t, []byte(readFixtureFile(t, config)))
	if args["audit-log-maxsize"] != "50" || args["audit-log-maxbackup"] != "10" || args["audit-policy-file"] != "/etc/bedrock/audit/policy.yaml" {
		t.Fatalf("extraArgs %v", args)
	}
}

func TestEnsureAuditArgsIsIdempotent(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "etc/k0s/k0s.yaml")
	writeFixtureFile(t, config, plainConfig)
	writeFixtureFile(t, policyPath(root), "policy")
	makeLogDir(t, root)
	if changed, err := ensureAuditArgs(root, controllerService()); err != nil || !changed {
		t.Fatalf("first run: %v %v", changed, err)
	}
	first := readFixtureFile(t, config)
	if changed, err := ensureAuditArgs(root, controllerService()); err != nil || changed || readFixtureFile(t, config) != first {
		t.Fatalf("second run: %v %v", changed, err)
	}
}

func (f authnArgsFixture) auditFilesAt(t *testing.T, modified time.Time) {
	t.Helper()
	writeFixtureFile(t, policyPath(f.deps.Root), "policy")
	touch(t, policyPath(f.deps.Root), modified)
	makeLogDir(t, f.deps.Root)
}

func (f authnArgsFixture) runsWithAllFlags(t *testing.T) {
	t.Helper()
	merged, _, err := withAPIServerArgs([]byte(plainConfig), apiserver.Args())
	if err != nil {
		t.Fatal(err)
	}
	merged, _, err = withAPIServerArgs(merged, audit.Args())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.deps.Root, "etc/k0s/k0s.yaml")
	writeFixtureFile(t, path, string(merged))
	touch(t, path, time.Now().Add(-2*time.Hour))
	apiserverCmdline(t, f.deps.Root, "--authentication-config=/etc/bedrock/authn/authentication.yaml", "--audit-policy-file=/etc/bedrock/audit/policy.yaml")
}

func TestAuditFlagsMissingFromTheRunningAPIServerWantARestart(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-audit-flags", v1alpha1.RoleControlPlane)
	f.runsWithFlagAndConfig(t)
	wanted, known := authnRestartWanted(f.deps.Root, controllerService())
	if wanted || !known {
		t.Fatalf("without audit files: wanted %v known %v", wanted, known)
	}
	f.auditFilesAt(t, time.Now().Add(-2*time.Hour))
	touch(t, filepath.Join(f.deps.Root, "etc/k0s/k0s.yaml"), time.Now())
	wanted, known = authnRestartWanted(f.deps.Root, controllerService())
	if !wanted || !known {
		t.Fatalf("audit flags missing and config newer: wanted %v known %v", wanted, known)
	}
}

func TestAuditPolicyChangeSetsRestartPending(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-audit-pending", v1alpha1.RoleControlPlane)
	f.runsWithAllFlags(t)
	f.auditFilesAt(t, time.Now().Add(-2*time.Hour))
	if wanted, known := authnRestartWanted(f.deps.Root, controllerService()); wanted || !known {
		t.Fatalf("an old policy file: wanted %v known %v", wanted, known)
	}
	touch(t, policyPath(f.deps.Root), time.Now())
	if wanted, known := authnRestartWanted(f.deps.Root, controllerService()); !wanted || !known {
		t.Fatalf("a policy file newer than the process: wanted %v known %v", wanted, known)
	}
	if got := authnRestartProblem(context.Background(), f.deps); got != "kube-apiserver still runs from before policy.yaml" {
		t.Fatalf("problem %q", got)
	}
}

func TestAuditPolicyChangeIsReportedInTheHostStatus(t *testing.T) {
	ctx := context.Background()
	f := newAuthnArgsFixture(t, "cp-audit-status", v1alpha1.RoleControlPlane)
	putAuthnSources(t, "audit.example")
	f.runsWithAllFlags(t)
	if err := Tick(ctx, k8sClient, f.deps); err != nil {
		t.Fatal(err)
	}
	longAgo := time.Now().Add(-2 * time.Hour)
	touch(t, filepath.Join(f.deps.Root, "etc/bedrock/authn/webhook.kubeconfig"), longAgo)
	touch(t, policyPath(f.deps.Root), longAgo)
	if err := Tick(ctx, k8sClient, f.deps); err != nil {
		t.Fatal(err)
	}
	if status := getHost(t, f.node).Status.Authn; status == nil || status.WebhookRestartPending {
		t.Fatalf("old files are not pending: %+v", status)
	}
	putAuditPolicy(t, validPolicy(t, "RequestResponse"))
	if err := Tick(ctx, k8sClient, f.deps); err != nil {
		t.Fatal(err)
	}
	if status := getHost(t, f.node).Status.Authn; status == nil || !status.WebhookRestartPending {
		t.Fatalf("a changed policy must be pending: %+v", status)
	}
}

func TestAuditPolicyChangeWaitsForTheRestartGrant(t *testing.T) {
	f := newAuthnArgsFixture(t, "cp-audit-grant", v1alpha1.RoleControlPlane)
	f.runsWithAllFlags(t)
	f.auditFilesAt(t, time.Now())
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
	if f.restarts() != 1 || !slices.Contains(f.exec.Calls, restartCall) {
		t.Fatalf("a fresh grant allows one restart: %v", f.exec.Calls)
	}
}

func TestK0sUpdateAddsAuditArgs(t *testing.T) {
	env, fake := k0sStepEnv(t, "k0s-audit-a", v1alpha1.RoleControlPlane)
	root := env.Deps.Root
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s"), "new k0s")
	fake.running = newK0s
	config := filepath.Join(root, "etc/k0s/k0s.yaml")
	writeFixtureFile(t, config, plainConfig)
	writeFixtureFile(t, policyPath(root), "policy")
	makeLogDir(t, root)
	outcome, err := k0sUpdate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) == 0 || fake.calls[0] != restartCall || outcome.Message != "k0s v1.36.3+k0s.0 installed, restarted k0scontroller.service" {
		t.Fatalf("a controller that gained the audit flags must restart: %+v %v", outcome, fake.calls)
	}
	args := extraArgsOf(t, []byte(readFixtureFile(t, config)))
	if args["audit-policy-file"] != "/etc/bedrock/audit/policy.yaml" || args["default-not-ready-toleration-seconds"] != "30" {
		t.Fatalf("extraArgs %v", args)
	}
}
