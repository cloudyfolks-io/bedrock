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
	sigyaml "sigs.k8s.io/yaml"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func createControlPlaneHost(t *testing.T, name string, roles ...string) {
	t.Helper()
	h := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.HostSpec{Roles: roles}}
	if err := k8sClient.Create(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), h) })
}

func setEtcdSchedule(t *testing.T, value string) {
	t.Helper()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: etcdScheduleKey}, Spec: v1alpha1.SettingSpec{Value: value}}
	if err := k8sClient.Create(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), setting) })
}

func snapshotDeps(t *testing.T, exec *fakeK0sBackup, now time.Time) Deps {
	t.Helper()
	deps := newDeps(nil, now)
	deps.Exec = exec
	deps.Root = t.TempDir()
	return deps
}

func snapshotFiles(t *testing.T, deps Deps, pattern string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(deps.Root, backupDir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	slices.Sort(names)
	return names
}

func at(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestEtcdSnapshotRunsWhenDue(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	setEtcdSchedule(t, "0 2 * * *")
	exec := &fakeK0sBackup{}
	deps := snapshotDeps(t, exec, at("2026-10-08T02:30:00Z"))
	for _, name := range []string{"ovnnb_db.db", "ovnsb_db.db"} {
		writeFixtureFile(t, filepath.Join(deps.Root, "etc/origin/ovn", name), name)
	}
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("k0s calls %v", exec.calls)
	}
	want := []string{"etcd-20261008T023000Z.tar.gz"}
	if got := snapshotFiles(t, deps, "*"); !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
	entries := archiveEntries(t, filepath.Join(deps.Root, backupDir, want[0]))
	if !slices.Equal(entries, []string{"k0s_backup_2026-10-01T10_00_00Z.tar.gz", "ovn/ovnnb_db.db", "ovn/ovnsb_db.db"}) {
		t.Fatalf("entries %v", entries)
	}
}

func TestEtcdSnapshotDueUsesTheNewestArchive(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	exec := &fakeK0sBackup{}
	deps := snapshotDeps(t, exec, at("2026-10-08T02:30:00Z"))
	writeFixtureFile(t, filepath.Join(deps.Root, backupDir, "etcd-20261008T020000Z.tar.gz"), "snapshot")
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("a snapshot of the same slot exists: %v", exec.calls)
	}
	deps.Now = func() time.Time { return at("2026-10-09T02:00:00Z") }
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("the next slot must run: %v", exec.calls)
	}
}

func TestEtcdSnapshotSkipsWorkers(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleWorkload)
	exec := &fakeK0sBackup{}
	deps := snapshotDeps(t, exec, at("2026-10-08T02:30:00Z"))
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 0 || len(snapshotFiles(t, deps, "*")) != 0 {
		t.Fatalf("worker took a snapshot: %v", exec.calls)
	}
}

func TestEtcdSnapshotKeepsSevenNewest(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	exec := &fakeK0sBackup{}
	deps := snapshotDeps(t, exec, at("2026-10-01T02:00:00Z"))
	for day := 0; day < 8; day++ {
		now := at("2026-10-01T02:00:00Z").AddDate(0, 0, day)
		deps.Now = func() time.Time { return now }
		if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
			t.Fatal(err)
		}
	}
	got := snapshotFiles(t, deps, "etcd-*.tar.gz")
	if len(got) != 7 || got[0] != "etcd-20261002T020000Z.tar.gz" || got[6] != "etcd-20261008T020000Z.tar.gz" {
		t.Fatalf("snapshots %v", got)
	}
}

func TestEtcdSnapshotKeepsUpgradeBackups(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	exec := &fakeK0sBackup{}
	deps := snapshotDeps(t, exec, at("2026-10-08T02:30:00Z"))
	upgrades := []string{"bedrock-v0.2.0-20260901T100000Z.tar.gz", "bedrock-v0.2.0-20260902T100000Z.tar.gz", "bedrock-v0.2.0-20260903T100000Z.tar.gz"}
	for _, name := range upgrades {
		writeFixtureFile(t, filepath.Join(deps.Root, backupDir, name), "upgrade")
	}
	for day := 0; day < 8; day++ {
		now := at("2026-10-01T02:00:00Z").AddDate(0, 0, day)
		deps.Now = func() time.Time { return now }
		if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
			t.Fatal(err)
		}
	}
	if got := snapshotFiles(t, deps, "bedrock-*.tar.gz"); !slices.Equal(got, upgrades) {
		t.Fatalf("upgrade backups %v", got)
	}
	deps.Now = func() time.Time { return at("2026-10-09T10:00:00Z") }
	env := StepEnv{Deps: deps, Upgrade: v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v0.3.0", From: "v0.2.0", Attempt: 1}}}
	if _, err := backup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFiles(t, deps, "etcd-*.tar.gz"); len(got) != 7 {
		t.Fatalf("an upgrade backup removed snapshots: %v", got)
	}
}

func TestEtcdSnapshotInvalidScheduleReturnsAnError(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	setEtcdSchedule(t, "@daily")
	exec := &fakeK0sBackup{}
	deps := snapshotDeps(t, exec, at("2026-10-08T02:30:00Z"))
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err == nil {
		t.Fatal("an invalid schedule must fail")
	}
	if len(exec.calls) != 0 {
		t.Fatalf("k0s calls %v", exec.calls)
	}
}

func TestEtcdSnapshotFailureLeavesNoPartialArchive(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	exec := &fakeK0sBackup{err: errors.New("etcd unavailable")}
	deps := snapshotDeps(t, exec, at("2026-10-08T02:30:00Z"))
	err := RunEtcdSnapshots(context.Background(), k8sClient, deps)
	if err == nil || !strings.Contains(err.Error(), "etcd unavailable") {
		t.Fatalf("error %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(deps.Root, backupDir)); len(entries) != 1 || entries[0].Name() != snapshotFailureFile {
		t.Fatalf("leftovers %v", entries)
	}
}

func TestAgentBackupRBACManifest(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "agent-backup-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	documents := strings.Split(string(body), "\n---\n")
	if len(documents) != 2 {
		t.Fatalf("documents %d", len(documents))
	}
	var role rbacv1.ClusterRole
	if err := sigyaml.Unmarshal([]byte(documents[0]), &role); err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 || !slices.Equal(role.Rules[0].Resources, []string{"settings"}) || !slices.Equal(role.Rules[0].Verbs, []string{"get"}) || !slices.Equal(role.Rules[0].ResourceNames, []string{"backup.etcd-schedule"}) || !slices.Equal(role.Rules[0].APIGroups, []string{"bedrock.cloudyfolks.io"}) {
		t.Fatalf("rules %+v", role.Rules)
	}
	var binding rbacv1.ClusterRoleBinding
	if err := sigyaml.Unmarshal([]byte(documents[1]), &binding); err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Name != role.Name || len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "Group" || binding.Subjects[0].Name != "system:nodes" {
		t.Fatalf("binding %+v", binding)
	}
}

func TestEtcdSnapshotIgnoresAndRemovesPartialFiles(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	exec := &fakeK0sBackup{}
	deps := snapshotDeps(t, exec, at("2026-10-08T02:30:00Z"))
	stale := filepath.Join(deps.Root, backupDir, "etcd-20261008T020000Z.tar.gz.partial")
	writeFixtureFile(t, stale, "truncated")
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("a partial file must not count as a snapshot: %v", exec.calls)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale partial file remains: %v", err)
	}
	if got := snapshotFiles(t, deps, "*"); !slices.Equal(got, []string{"etcd-20261008T023000Z.tar.gz"}) {
		t.Fatalf("files %v", got)
	}
}

func TestUpgradeBackupLeavesNoPartialFile(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{})
	if _, err := backup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(env.Deps.Root, backupDir)
	if partials, _ := filepath.Glob(filepath.Join(dir, "*.partial")); len(partials) != 0 {
		t.Fatalf("partial files %v", partials)
	}
}

func TestEtcdBackoffDoublesUpToTheInterval(t *testing.T) {
	cases := []struct {
		attempts int
		interval time.Duration
		want     time.Duration
	}{
		{1, 24 * time.Hour, time.Minute},
		{2, 24 * time.Hour, 2 * time.Minute},
		{3, 24 * time.Hour, 4 * time.Minute},
		{12, 24 * time.Hour, 24 * time.Hour},
		{5, time.Hour, 16 * time.Minute},
		{8, time.Hour, time.Hour},
		{500, time.Hour, time.Hour},
	}
	for _, tc := range cases {
		if got := etcdBackoff(tc.attempts, tc.interval); got != tc.want {
			t.Fatalf("attempts %d: got %s want %s", tc.attempts, got, tc.want)
		}
	}
}

func TestEtcdSnapshotWaitsAfterAFailure(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	exec := &fakeK0sBackup{err: errors.New("etcd unavailable")}
	start := at("2026-10-08T02:30:00Z")
	deps := snapshotDeps(t, exec, start)
	at := func(offset time.Duration) { deps.Now = func() time.Time { return start.Add(offset) } }
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err == nil {
		t.Fatal("the first failure must be reported")
	}
	at(30 * time.Second)
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil || len(exec.calls) != 1 {
		t.Fatalf("must wait one minute: %v, calls %v", err, exec.calls)
	}
	at(61 * time.Second)
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err == nil || len(exec.calls) != 2 {
		t.Fatalf("must retry after the wait: %v, calls %v", err, exec.calls)
	}
	at(61*time.Second + 90*time.Second)
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil || len(exec.calls) != 2 {
		t.Fatalf("must wait two minutes now: %v, calls %v", err, exec.calls)
	}
	exec.err = nil
	at(61*time.Second + 121*time.Second)
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFiles(t, deps, ".etcd-snapshot-failure"); len(got) != 0 {
		t.Fatalf("a success must clear the failure marker: %v", got)
	}
	if len(snapshotFiles(t, deps, "etcd-*.tar.gz")) != 1 {
		t.Fatal("the snapshot must exist after the retry")
	}
}
