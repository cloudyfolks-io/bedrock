package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

type applyFixture struct {
	deps    Deps
	exec    *host.FakeExec
	node    string
	current []byte
	target  []byte
	restart *int
}

func secretReadCall(root string) string {
	return "/usr/local/bin/k0s kubectl --kubeconfig " + filepath.Join(root, "var/lib/k0s/pki/admin.conf") + " --request-timeout=30s --namespace kube-system get secret bedrock-secrets-encryption -o json"
}

func localReadyzCall(root string) string {
	return "/usr/local/bin/k0s kubectl --kubeconfig " + filepath.Join(root, "var/lib/k0s/pki/admin.conf") + " --server https://127.0.0.1:6443 --request-timeout=10s get --raw /readyz"
}

func encryptionSecretJSON(t *testing.T, content []byte) string {
	t.Helper()
	raw, err := json.Marshal(corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, Data: map[string][]byte{"encryption.yaml": content}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func newApplyFixture(t *testing.T, node string, current, target []byte, grant string) applyFixture {
	t.Helper()
	h := hostWithRole(node, v1alpha1.RoleControlPlane)
	if grant != "" {
		h.Annotations = map[string]string{v1alpha1.AnnotationEncryptionApply: grant}
	}
	if err := k8sClient.Create(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), &h) })
	exec := &host.FakeExec{Responses: map[string]string{restartCall: ""}}
	deps := authnDeps(t, time.Now())
	deps.Node = node
	deps.K0sTimeout = 300 * time.Millisecond
	deps.K0sPoll = 10 * time.Millisecond
	deps.ProbeTimeout = time.Second
	root := deps.Root
	exec.Responses[localReadyzCall(root)] = "ok"
	exec.Responses[secretReadCall(root)] = encryptionSecretJSON(t, target)
	writeFixtureFile(t, encryptionPath(root), string(current))
	touch(t, encryptionPath(root), time.Now().Add(-2*time.Hour))
	fakeAPIServer(t, root, time.Now().Add(-time.Hour))
	restarts := 0
	deps.Exec = restartHost{FakeExec: exec, onRestart: func() {
		restarts++
		fakeAPIServer(t, root, time.Now().Add(time.Duration(restarts)*time.Minute))
	}}
	deps.AuthnOwner = func(string) error { return nil }
	return applyFixture{deps: deps, exec: exec, node: node, current: current, target: target, restart: &restarts}
}

func (f applyFixture) file(t *testing.T) string {
	t.Helper()
	return readFixtureFile(t, encryptionPath(f.deps.Root))
}

func (f applyFixture) retired(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.deps.Root, "etc/bedrock/encryption/retired"))
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

type applyConfigs struct{ one, added, promoted, removed []byte }

func newApplyConfigs(t *testing.T) applyConfigs {
	t.Helper()
	one, err := encryption.NewConfig(rand.Reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	added, _ := encryption.AddKey(one, "key2", rand.Reader)
	promoted, _ := encryption.Promote(added, "key2")
	removed, _ := encryption.RemoveKey(promoted, "key1")
	return applyConfigs{one: one, added: added, promoted: promoted, removed: removed}
}

func TestApplyEncryptionWithoutAGrantDoesNothing(t *testing.T) {
	cfg := newApplyConfigs(t)
	f := newApplyFixture(t, "cp-enc-nogrant", cfg.one, cfg.added, "")
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err != nil {
		t.Fatal(err)
	}
	if len(f.exec.Calls) != 0 || f.file(t) != string(cfg.one) {
		t.Fatalf("calls %v", f.exec.Calls)
	}
}

func TestApplyEncryptionIgnoresAGrantForAnotherContent(t *testing.T) {
	cfg := newApplyConfigs(t)
	f := newApplyFixture(t, "cp-enc-stale", cfg.one, cfg.added, encryption.Hash(cfg.promoted))
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err != nil {
		t.Fatal(err)
	}
	if *f.restart != 0 || f.file(t) != string(cfg.one) {
		t.Fatalf("a stale grant must change nothing: %v", f.exec.Calls)
	}
}

func TestApplyEncryptionWritesTheFileAndRestartsOnce(t *testing.T) {
	cfg := newApplyConfigs(t)
	f := newApplyFixture(t, "cp-enc-apply", cfg.one, cfg.added, encryption.Hash(cfg.added))
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(encryptionPath(f.deps.Root))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info, err)
	}
	if f.file(t) != string(cfg.added) || *f.restart != 1 || len(f.retired(t)) != 0 {
		t.Fatalf("restarts %d retired %v", *f.restart, f.retired(t))
	}
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err != nil || *f.restart != 1 {
		t.Fatalf("a converged host must not restart again: %v restarts %d", err, *f.restart)
	}
	if got := syncEncryptionStatus(f.deps, hostWithRole(f.node, v1alpha1.RoleControlPlane)); got.RestartPending || got.Hash != encryption.Hash(cfg.added) {
		t.Fatalf("status %+v", got)
	}
}

func TestApplyEncryptionKeepsTheDroppedKeyInRetired(t *testing.T) {
	cfg := newApplyConfigs(t)
	f := newApplyFixture(t, "cp-enc-retire", cfg.promoted, cfg.removed, encryption.Hash(cfg.removed))
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err != nil {
		t.Fatal(err)
	}
	names := f.retired(t)
	if len(names) != 1 || !strings.HasSuffix(names[0], ".yaml") {
		t.Fatalf("retired %v", names)
	}
	path := filepath.Join(f.deps.Root, "etc/bedrock/encryption/retired", names[0])
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || readFixtureFile(t, path) != string(cfg.promoted) {
		t.Fatalf("retired file %v err %v", info, err)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("retired dir %v err %v", dir, err)
	}
	parent, err := os.Stat(filepath.Dir(encryptionPath(f.deps.Root)))
	if err != nil || parent.Mode().Perm() != 0o700 {
		t.Fatalf("the encryption dir must stay private: %v err %v", parent, err)
	}
}

func TestApplyEncryptionRestoresTheOldFileWhenTheAPIServerDoesNotReturn(t *testing.T) {
	cfg := newApplyConfigs(t)
	f := newApplyFixture(t, "cp-enc-fail", cfg.promoted, cfg.removed, encryption.Hash(cfg.removed))
	root := f.deps.Root
	attempts := 0
	f.deps.Exec = restartHost{FakeExec: f.exec, onRestart: func() {
		attempts++
		if attempts == 2 {
			fakeAPIServer(t, root, time.Now().Add(time.Hour))
		}
	}}
	err := ApplyEncryption(context.Background(), k8sClient, f.deps)
	if err == nil || !strings.Contains(err.Error(), "did not restart") {
		t.Fatalf("error %v", err)
	}
	if f.file(t) != string(cfg.promoted) || len(f.retired(t)) != 0 {
		t.Fatalf("the old file must return and the retired copy must go: retired %v", f.retired(t))
	}
	if attempts != 2 {
		t.Fatalf("one restart for the new file and one for the old: %d", attempts)
	}
	if readEncryptionFailure(root) != encryption.Hash(cfg.removed) {
		t.Fatalf("failed hash %q", readEncryptionFailure(root))
	}
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err != nil || attempts != 2 {
		t.Fatalf("a failed hash must not run again: %v attempts %d", err, attempts)
	}
}

func TestApplyEncryptionFinishesTheRestartOfAWrittenFile(t *testing.T) {
	cfg := newApplyConfigs(t)
	f := newApplyFixture(t, "cp-enc-resume", cfg.added, cfg.added, encryption.Hash(cfg.added))
	touch(t, encryptionPath(f.deps.Root), time.Now())
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err != nil {
		t.Fatal(err)
	}
	if *f.restart != 1 || f.file(t) != string(cfg.added) {
		t.Fatalf("restarts %d", *f.restart)
	}
}

func TestApplyEncryptionWaitsForTheNodeUpgrade(t *testing.T) {
	cfg := newApplyConfigs(t)
	f := newApplyFixture(t, "cp-enc-upgrade", cfg.one, cfg.added, encryption.Hash(cfg.added))
	upgrade := &v1alpha1.NodeUpgrade{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v2", "cp-enc-upgrade")}, Spec: v1alpha1.NodeUpgradeSpec{Node: "cp-enc-upgrade", Version: "v2", From: "v1", Attempt: 1, Steps: []string{v1alpha1.StepK0sUpdate}}}
	if err := k8sClient.Create(context.Background(), upgrade); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), upgrade) })
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err != nil {
		t.Fatal(err)
	}
	if *f.restart != 0 || f.file(t) != string(cfg.one) {
		t.Fatalf("an upgrade owns the node: %v", f.exec.Calls)
	}
}

func TestApplyEncryptionIgnoresWorkers(t *testing.T) {
	cfg := newApplyConfigs(t)
	h := hostWithRole("worker-enc", v1alpha1.RoleWorkload)
	h.Annotations = map[string]string{v1alpha1.AnnotationEncryptionApply: encryption.Hash(cfg.added)}
	if err := k8sClient.Create(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), &h) })
	deps := authnDeps(t, time.Now())
	deps.Node = "worker-enc"
	exec := &host.FakeExec{}
	deps.Exec = exec
	if err := ApplyEncryption(context.Background(), k8sClient, deps); err != nil || len(exec.Calls) != 0 {
		t.Fatalf("err %v calls %v", err, exec.Calls)
	}
}

func TestApplyEncryptionDoesNotTrustAnotherNodeToAnswerReadyz(t *testing.T) {
	cfg := newApplyConfigs(t)
	f := newApplyFixture(t, "cp-enc-vip", cfg.one, cfg.added, encryption.Hash(cfg.added))
	delete(f.exec.Responses, localReadyzCall(f.deps.Root))
	f.exec.Responses[readyzCall(f.deps.Root)] = "ok"
	err := ApplyEncryption(context.Background(), k8sClient, f.deps)
	if err == nil || !strings.Contains(err.Error(), "did not restart") {
		t.Fatalf("a readyz answer of the VIP must not count: %v", err)
	}
	if f.file(t) != string(cfg.one) {
		t.Fatal("the old file must return")
	}
}

func handledGrant(t *testing.T, f applyFixture) string {
	t.Helper()
	return syncEncryptionStatus(f.deps, hostWithRole(f.node, v1alpha1.RoleControlPlane)).HandledGrant
}

func TestApplyEncryptionReportsTheGrantItHandled(t *testing.T) {
	cfg := newApplyConfigs(t)
	applied := newApplyFixture(t, "cp-enc-handled-ok", cfg.one, cfg.added, encryption.Hash(cfg.added))
	if err := ApplyEncryption(context.Background(), k8sClient, applied.deps); err != nil || handledGrant(t, applied) != encryption.Hash(cfg.added) {
		t.Fatalf("applied: %v handled %q", err, handledGrant(t, applied))
	}
	stale := newApplyFixture(t, "cp-enc-handled-stale", cfg.one, cfg.added, encryption.Hash(cfg.promoted))
	if err := ApplyEncryption(context.Background(), k8sClient, stale.deps); err != nil || handledGrant(t, stale) != encryption.Hash(cfg.promoted) {
		t.Fatalf("stale: %v handled %q", err, handledGrant(t, stale))
	}
	failed := newApplyFixture(t, "cp-enc-handled-fail", cfg.promoted, cfg.removed, encryption.Hash(cfg.removed))
	failed.deps.Exec = restartHost{FakeExec: failed.exec, onRestart: func() {}}
	if err := ApplyEncryption(context.Background(), k8sClient, failed.deps); err == nil || handledGrant(t, failed) != encryption.Hash(cfg.removed) {
		t.Fatalf("failed: %v handled %q", err, handledGrant(t, failed))
	}
}

func TestApplyEncryptionRetriesAfterTheFailureMarkerIsDeleted(t *testing.T) {
	cfg := newApplyConfigs(t)
	f := newApplyFixture(t, "cp-enc-retry", cfg.promoted, cfg.removed, encryption.Hash(cfg.removed))
	root := f.deps.Root
	attempts := 0
	f.deps.Exec = restartHost{FakeExec: f.exec, onRestart: func() {
		attempts++
		if attempts == 2 || attempts >= 3 {
			fakeAPIServer(t, root, time.Now().Add(time.Duration(attempts)*time.Hour))
		}
	}}
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err == nil {
		t.Fatal("the first try must fail")
	}
	if err := os.Remove(filepath.Join(root, "etc/bedrock/encryption/.apply-failed")); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEncryption(context.Background(), k8sClient, f.deps); err != nil {
		t.Fatalf("the grant must run again: %v", err)
	}
	if f.file(t) != string(cfg.removed) {
		t.Fatal("the new file must stay after the retry")
	}
}
