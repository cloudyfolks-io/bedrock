package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	oldOnly  = "quay.io/a/old:1@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newOnly  = "quay.io/a/new:2@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	shared   = "quay.io/a/shared:1@sha256:3333333333333333333333333333333333333333333333333333333333333333"
	retagged = "quay.io/a/shared:1.0.1@sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

func namesOf(t *testing.T, refs ...string) []string {
	t.Helper()
	var names []string
	for _, ref := range refs {
		refNames, err := release.ContainerdNames(ref)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, refNames...)
	}
	return names
}

func finishEnv(t *testing.T, present []string) (StepEnv, *fakeContainerd) {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, "run/k0s/containerd.sock"), "")
	writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/staged/v0.3.0/k0s"), "new k0s")
	writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/previous/k0s"), "old k0s")
	for _, version := range []string{"v0.2.0", "v0.3.0"} {
		writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/depot", version, "amd64", "bundle.yaml"), "version: "+version+"\n")
	}
	container := &fakeContainerd{present: present, failures: map[string]error{}}
	deps := newDeps(nil, time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC))
	deps.Exec = removingContainerd{container}
	deps.Root = root
	env := StepEnv{
		Deps:    deps,
		Upgrade: v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v0.3.0", From: "v0.2.0", Attempt: 1}},
		Target:  v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.3.0"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.3.0", Images: []string{newOnly, retagged}}},
		From:    v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.2.0"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.2.0", Images: []string{oldOnly, shared}}},
	}
	return env, container
}

type removingContainerd struct {
	*fakeContainerd
}

func (r removingContainerd) Run(ctx context.Context, name string, args ...string) (string, error) {
	if len(args) == 6 && args[4] == "rm" {
		r.calls = append(r.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
		r.present = slices.DeleteFunc(r.present, func(image string) bool { return image == args[5] })
		return "", nil
	}
	return r.fakeContainerd.Run(ctx, name, args...)
}

func removed(container *fakeContainerd) []string {
	var images []string
	for _, call := range container.calls {
		if strings.Contains(call, " images rm ") {
			images = append(images, call[strings.LastIndex(call, " ")+1:])
		}
	}
	return images
}

func TestPruneRemovesOnlyOldImagesAndFiles(t *testing.T) {
	env, container := finishEnv(t, namesOf(t, oldOnly, newOnly, shared, retagged))
	outcome, err := prune(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	want := namesOf(t, oldOnly)
	want = append(want, "quay.io/a/shared:1")
	slices.Sort(want)
	got := removed(container)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("removed %v, want %v: the shared digest name must stay", got, want)
	}
	for _, path := range []string{"var/lib/bedrock/staged/v0.3.0", "var/lib/bedrock/previous/k0s", "var/lib/bedrock/depot/v0.2.0"} {
		if _, err := os.Stat(filepath.Join(env.Deps.Root, path)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed", path)
		}
	}
	if _, err := os.Stat(filepath.Join(env.Deps.Root, "var/lib/bedrock/depot/v0.3.0")); err != nil {
		t.Fatal("the current depot version must stay")
	}
	if outcome.Message != "removed 3 images" {
		t.Fatalf("message %q", outcome.Message)
	}
}

func TestCleanupRemovesTargetOnlyImagesAndKeepsTheDepot(t *testing.T) {
	env, container := finishEnv(t, namesOf(t, oldOnly, newOnly, shared, retagged))
	if _, err := cleanup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	want := namesOf(t, newOnly)
	want = append(want, "quay.io/a/shared:1.0.1")
	slices.Sort(want)
	got := removed(container)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("removed %v, want %v", got, want)
	}
	for _, path := range []string{"var/lib/bedrock/staged/v0.3.0", "var/lib/bedrock/previous/k0s"} {
		if _, err := os.Stat(filepath.Join(env.Deps.Root, path)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed", path)
		}
	}
	for _, version := range []string{"v0.2.0", "v0.3.0"} {
		if _, err := os.Stat(filepath.Join(env.Deps.Root, "var/lib/bedrock/depot", version)); err != nil {
			t.Fatalf("cleanup must keep depot %s for a new attempt", version)
		}
	}
}

func TestCleanupRemovesPreRestoreData(t *testing.T) {
	env, _ := finishEnv(t, nil)
	root := env.Deps.Root
	for _, holder := range []string{".pre-restore-20261001T110000Z", ".pre-restore-20261002T090000Z"} {
		writeFixtureFile(t, filepath.Join(root, "var/lib/k0s", holder, "etcd", "data"), "old etcd")
	}
	writeFixtureFile(t, filepath.Join(root, "var/lib/k0s/etcd/data"), "etcd")
	if _, err := cleanup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	leftover, err := filepath.Glob(filepath.Join(root, "var/lib/k0s/.pre-restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftover) != 0 {
		t.Fatalf("cleanup must remove the data a restore moved aside: %v", leftover)
	}
	if readFixtureFile(t, filepath.Join(root, "var/lib/k0s/etcd/data")) != "etcd" {
		t.Fatal("the live k0s data must stay")
	}
}

func TestCleanupTriesEveryRemovalAfterAFailure(t *testing.T) {
	env, container := finishEnv(t, namesOf(t, oldOnly, newOnly, shared, retagged))
	container.failures["/usr/local/bin/k0s ctr --namespace k8s.io images ls --quiet"] = errors.New("containerd is down")
	root := env.Deps.Root
	writeArchived(t, root, "v0.2.0", "k0s-airgap.tar")
	writeArchived(t, root, "v0.3.0", "k0s-airgap.tar")
	writeFixtureFile(t, filepath.Join(root, "var/lib/k0s/.pre-restore-20261001T110000Z/etcd/data"), "old etcd")
	if _, err := cleanup(context.Background(), env); err == nil || !strings.Contains(err.Error(), "containerd is down") {
		t.Fatalf("error %v", err)
	}
	for _, path := range []string{"var/lib/bedrock/images/v0.3.0", "var/lib/bedrock/staged/v0.3.0", "var/lib/bedrock/previous/k0s", "var/lib/k0s/.pre-restore-20261001T110000Z"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed although the image removal failed", path)
		}
	}
}

func writeImageTarball(t *testing.T, root, name string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(root, k0sImagesDir, name), "tar")
}

func writeArchived(t *testing.T, root, version string, names ...string) {
	t.Helper()
	for _, name := range names {
		writeFixtureFile(t, archiveFile(root, version, name), "tar")
	}
}

func TestPruneKeepsTargetTarballsCarriesSharedOnesAndDropsTheOldVersion(t *testing.T) {
	env, _ := finishEnv(t, nil)
	root := env.Deps.Root
	writeArchived(t, root, "v0.2.0", release.ImageFileName(oldOnly), release.ImageFileName(shared), "k0s-airgap.tar")
	writeArchived(t, root, "v0.3.0", release.ImageFileName(newOnly), release.ImageFileName(retagged), "k0s-airgap.tar")
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, archiveDir, "v0.2.0")) {
		t.Fatal("the old version must leave the archive")
	}
	for _, kept := range []string{release.ImageFileName(newOnly), release.ImageFileName(retagged), "k0s-airgap.tar"} {
		if !exists(archiveFile(root, "v0.3.0", kept)) {
			t.Fatalf("%s must be kept", kept)
		}
	}
}

func TestPruneKeepsTheOldVersionWhenTheTargetIsNotArchived(t *testing.T) {
	env, _ := finishEnv(t, nil)
	root := env.Deps.Root
	writeArchived(t, root, "v0.2.0", release.ImageFileName(oldOnly))
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if !exists(archiveFile(root, "v0.2.0", release.ImageFileName(oldOnly))) {
		t.Fatal("the last copy of a tarball must stay")
	}
}

func TestCleanupDropsTheTargetVersionAndKeepsTheOldOne(t *testing.T) {
	env, _ := finishEnv(t, nil)
	root := env.Deps.Root
	writeArchived(t, root, "v0.2.0", release.ImageFileName(oldOnly), release.ImageFileName(shared), "k0s-airgap.tar")
	writeArchived(t, root, "v0.3.0", release.ImageFileName(newOnly), release.ImageFileName(retagged), "k0s-airgap.tar")
	if _, err := cleanup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, archiveDir, "v0.3.0")) {
		t.Fatal("the target version must leave the archive")
	}
	for _, kept := range []string{release.ImageFileName(oldOnly), release.ImageFileName(shared), "k0s-airgap.tar"} {
		if !exists(archiveFile(root, "v0.2.0", kept)) {
			t.Fatalf("%s must be kept", kept)
		}
	}
}

func TestPruneWithoutContainersRemovesFilesOnly(t *testing.T) {
	env, container := finishEnv(t, nil)
	if err := os.Remove(filepath.Join(env.Deps.Root, "run/k0s/containerd.sock")); err != nil {
		t.Fatal(err)
	}
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if len(container.calls) != 0 {
		t.Fatalf("a host without containerd must run no ctr command: %v", container.calls)
	}
}

const (
	sealedBackupPath    = "/var/lib/bedrock/backups/bedrock-v0.2.0-20261001T100000Z.tar.gz.age"
	plaintextBackupPath = "/var/lib/bedrock/backups/bedrock-v0.2.0-20261001T100000Z.tar.gz"
)

func restoreWork(t *testing.T, extra ...string) string {
	t.Helper()
	work := t.TempDir()
	writeFixtureFile(t, filepath.Join(work, "k0s_backup_2026-10-01T10_00_00Z.tar.gz"), "etcd snapshot")
	writeFixtureFile(t, filepath.Join(work, "ovn", "ovnnb_db.db"), "nb")
	for _, name := range extra {
		noise := make([]byte, 256<<10)
		if _, err := rand.Read(noise); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, filepath.Join(work, name), string(noise))
	}
	return work
}

func restoreEnvAt(t *testing.T, exec *host.FakeExec, backupPath string) StepEnv {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/lib/bedrock/backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/previous/k0s"), "old k0s")
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s"), "new k0s")
	for _, dir := range []string{"etcd", "pki", "manifests", "images", "containerd"} {
		writeFixtureFile(t, filepath.Join(root, "var/lib/k0s", dir, "data"), dir)
	}
	deps := newDeps(exec, time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC))
	deps.Root = root
	deps.K0sTimeout = time.Second
	deps.K0sPoll = time.Millisecond
	return StepEnv{Deps: deps, Client: k8sClient, Upgrade: v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v0.3.0", From: "v0.2.0", Attempt: 1, Backup: backupPath}}}
}

func sealedRestoreEnvFrom(t *testing.T, exec *host.FakeExec, work string) StepEnv {
	t.Helper()
	env := restoreEnvAt(t, exec, sealedBackupPath)
	recipient := hostIdentity(t, env.Deps.Root).Recipient()
	if err := writeArchive(work, filepath.Join(env.Deps.Root, sealedBackupPath), []age.Recipient{recipient}); err != nil {
		t.Fatal(err)
	}
	return env
}

func restoreEnv(t *testing.T, exec *host.FakeExec) StepEnv {
	t.Helper()
	return sealedRestoreEnvFrom(t, exec, restoreWork(t))
}

func plaintextRestoreEnv(t *testing.T, exec *host.FakeExec) StepEnv {
	t.Helper()
	env := restoreEnvAt(t, exec, plaintextBackupPath)
	file, err := os.Create(filepath.Join(env.Deps.Root, plaintextBackupPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(writeTarGz(file, restoreWork(t)), file.Close()); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestRestoreRollsBackK0sAndEtcd(t *testing.T) {
	env := restoreEnv(t, &host.FakeExec{})
	exec := restoredClusterExec(env.Deps.Root, `{"items":[]}`)
	env.Deps.Exec = exec
	outcome, err := restore(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	root := env.Deps.Root
	if readFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s")) != "old k0s" {
		t.Fatal("the previous k0s binary must be back")
	}
	if len(exec.Calls) < 3 || exec.Calls[0] != "systemctl stop k0scontroller.service" || !strings.HasPrefix(exec.Calls[1], "/usr/local/bin/k0s restore --config-out ") || !strings.HasSuffix(exec.Calls[1], "k0s_backup_2026-10-01T10_00_00Z.tar.gz") || exec.Calls[2] != "systemctl start k0scontroller.service" {
		t.Fatalf("calls %v", exec.Calls)
	}
	for _, dir := range []string{"etcd", "pki", "manifests", "images"} {
		if _, err := os.Stat(filepath.Join(root, "var/lib/k0s", dir)); !os.IsNotExist(err) {
			t.Fatalf("%s overlaps the backup and must be moved aside", dir)
		}
		if _, err := os.Stat(filepath.Join(root, "var/lib/k0s/.pre-restore-20261001T110000Z", dir, "data")); err != nil {
			t.Fatalf("%s must be kept in .pre-restore: %v", dir, err)
		}
	}
	if readFixtureFile(t, filepath.Join(root, "var/lib/k0s/containerd/data")) != "containerd" {
		t.Fatal("the containerd store must stay in place, offline images live there")
	}
	var marker v1alpha1.RestoreStatus
	if err := json.Unmarshal([]byte(readFixtureFile(t, filepath.Join(root, "var/lib/bedrock/restore.json"))), &marker); err != nil {
		t.Fatal(err)
	}
	if marker.Backup != env.Upgrade.Spec.Backup || !marker.CompletedAt.Time.Equal(time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("marker %+v", marker)
	}
	if outcome.Message != "restored "+env.Upgrade.Spec.Backup+", workloads restarted: 0" {
		t.Fatalf("message %q", outcome.Message)
	}
}

func TestRestoreReadsPlaintextArchive(t *testing.T) {
	env := plaintextRestoreEnv(t, &host.FakeExec{})
	exec := restoredClusterExec(env.Deps.Root, `{"items":[]}`)
	env.Deps.Exec = exec
	outcome, err := restore(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.Calls) < 3 || exec.Calls[0] != "systemctl stop k0scontroller.service" || !strings.HasSuffix(exec.Calls[1], "k0s_backup_2026-10-01T10_00_00Z.tar.gz") {
		t.Fatalf("calls %v", exec.Calls)
	}
	if outcome.Message != "restored "+plaintextBackupPath+", workloads restarted: 0" {
		t.Fatalf("message %q", outcome.Message)
	}
}

func requireRestoreRefusedBeforeStop(t *testing.T, env StepEnv, exec *host.FakeExec) error {
	t.Helper()
	_, err := restore(context.Background(), env)
	if err == nil || !strings.HasPrefix(err.Error(), "open backup "+env.Upgrade.Spec.Backup+": ") {
		t.Fatalf("error %v", err)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("k0s must keep running, calls %v", exec.Calls)
	}
	if readFixtureFile(t, filepath.Join(env.Deps.Root, "usr/local/bin/k0s")) != "new k0s" {
		t.Fatal("the k0s binary must stay")
	}
	if _, err := os.Stat(filepath.Join(env.Deps.Root, "var/lib/k0s/etcd/data")); err != nil {
		t.Fatalf("etcd must stay in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.Deps.Root, "var/lib/bedrock/restore.json")); !os.IsNotExist(err) {
		t.Fatalf("no restore marker may exist: %v", err)
	}
	return err
}

func TestRestoreFailsBeforeStopWithoutHostKey(t *testing.T) {
	exec := &host.FakeExec{}
	env := restoreEnv(t, exec)
	if err := os.Remove(filepath.Join(env.Deps.Root, "etc/bedrock/backup/host.key")); err != nil {
		t.Fatal(err)
	}
	requireRestoreRefusedBeforeStop(t, env, exec)
	if _, err := os.Stat(filepath.Join(env.Deps.Root, "etc/bedrock/backup/host.key")); !os.IsNotExist(err) {
		t.Fatalf("a restore must not create a host key: %v", err)
	}
}

func TestRestoreFailsBeforeStopWithAnotherHostKey(t *testing.T) {
	exec := &host.FakeExec{}
	env := restoreEnv(t, exec)
	other := newRecipient(t)
	if err := os.WriteFile(filepath.Join(env.Deps.Root, "etc/bedrock/backup/host.key"), seal.KeyFile(other), 0o600); err != nil {
		t.Fatal(err)
	}
	requireRestoreRefusedBeforeStop(t, env, exec)
}

func TestRestoreFailsBeforeStopOnACorruptSealedArchive(t *testing.T) {
	exec := &host.FakeExec{}
	env := restoreEnv(t, exec)
	archive := filepath.Join(env.Deps.Root, sealedBackupPath)
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-3] ^= 0xff
	if err := os.WriteFile(archive, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	requireRestoreRefusedBeforeStop(t, env, exec)
}

func TestRestoreFailsBeforeStopOnATruncatedSealedArchive(t *testing.T) {
	exec := &host.FakeExec{}
	env := sealedRestoreEnvFrom(t, exec, restoreWork(t, "ovn/ovnsb_db.db"))
	archive := filepath.Join(env.Deps.Root, sealedBackupPath)
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, raw[:len(raw)-1000], 0o600); err != nil {
		t.Fatal(err)
	}
	requireRestoreRefusedBeforeStop(t, env, exec)
}

func TestRestoreFailsBeforeStopWhenTheArchiveIsMissing(t *testing.T) {
	exec := &host.FakeExec{}
	env := restoreEnv(t, exec)
	if err := os.Remove(filepath.Join(env.Deps.Root, sealedBackupPath)); err != nil {
		t.Fatal(err)
	}
	err := requireRestoreRefusedBeforeStop(t, env, exec)
	want := "open backup " + sealedBackupPath + ": no such file or directory"
	if err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
}

func TestRestoreFailsBeforeStopWhenTheArchiveHoldsNoK0sBackup(t *testing.T) {
	exec := &host.FakeExec{}
	work := t.TempDir()
	writeFixtureFile(t, filepath.Join(work, "ovn", "ovnnb_db.db"), "nb")
	env := sealedRestoreEnvFrom(t, exec, work)
	err := requireRestoreRefusedBeforeStop(t, env, exec)
	want := "open backup " + sealedBackupPath + ": no k0s backup in the archive"
	if err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
}

func TestRestoreWritesTheMarkerBeforeK0sStarts(t *testing.T) {
	exec := &host.FakeExec{Responses: map[string]string{"systemctl stop k0scontroller.service": ""}, Errors: map[string]error{"systemctl start k0scontroller.service": errors.New("exit status 1")}, ResponsePrefixes: map[string]string{"/usr/local/bin/k0s restore --config-out ": ""}}
	env := restoreEnv(t, exec)
	if _, err := restore(context.Background(), env); err == nil || err.Error() != "exit status 1" {
		t.Fatalf("error %v", err)
	}
	if exec.Calls[len(exec.Calls)-1] != "systemctl start k0scontroller.service" {
		t.Fatalf("calls %v", exec.Calls)
	}
	if _, err := os.Stat(filepath.Join(env.Deps.Root, "var/lib/bedrock/restore.json")); err != nil {
		t.Fatalf("the restore marker must be written before k0s starts: %v", err)
	}
}

func TestRestoreNeedsABackup(t *testing.T) {
	env := restoreEnv(t, &host.FakeExec{})
	env.Upgrade.Spec.Backup = ""
	if _, err := restore(context.Background(), env); err == nil || err.Error() != "no backup to restore" {
		t.Fatalf("error %v", err)
	}
}

func TestFinishStepsAreRegistered(t *testing.T) {
	for _, name := range []string{v1alpha1.StepPrune, v1alpha1.StepCleanup, v1alpha1.StepRestore} {
		if _, ok := Steps()[name]; !ok {
			t.Fatalf("Steps must include %s", name)
		}
	}
}

func TestCleanupRemovesTheTargetArchiveWhenTheOldOneIsMissing(t *testing.T) {
	env, _ := finishEnv(t, nil)
	root := env.Deps.Root
	writeArchived(t, root, "v0.3.0", release.ImageFileName(newOnly), release.ImageFileName(shared))
	if _, err := cleanup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, archiveDir, "v0.3.0")) {
		t.Fatal("target-only tarballs must not stay after an abort")
	}
	if !exists(archiveFile(root, "v0.2.0", release.ImageFileName(shared))) {
		t.Fatal("the shared tarball must move to the old version")
	}
}

func TestPruneKeepsTheArchiveWhenContainerdCannotBeListed(t *testing.T) {
	env, container := finishEnv(t, nil)
	root := env.Deps.Root
	writeArchived(t, root, "v0.2.0", release.ImageFileName(oldOnly))
	writeArchived(t, root, "v0.3.0", release.ImageFileName(newOnly))
	container.failures["/usr/local/bin/k0s ctr --namespace k8s.io images ls"] = errors.New("containerd is down")
	if _, err := prune(context.Background(), env); err == nil {
		t.Fatal("the failure must be reported")
	}
	if !exists(archiveFile(root, "v0.2.0", release.ImageFileName(oldOnly))) {
		t.Fatal("tarballs must stay until their images are unpinned, so a retry converges")
	}
}

func TestRetainedTarballsAreTheKeptListIncludingCarriedFiles(t *testing.T) {
	before := []archivedTarball{
		{Version: "v0.2.0", File: "shared.tar"},
		{Version: "v0.2.0", File: "old.tar"},
		{Version: "v0.2.0", File: "k0s-airgap.tar"},
		{Version: "v0.3.0", File: "new.tar"},
		{Version: "v0.3.0", File: "stale.tar"},
		{Version: "v0.3.0", File: "k0s-airgap.tar"},
	}
	got := retainedTarballs(before, "v0.3.0", []string{"shared.tar", "new.tar", "k0s-airgap.tar"})
	var files []string
	for _, t := range got {
		files = append(files, t.Version+"/"+t.File)
	}
	slices.Sort(files)
	want := []string{"v0.2.0/shared.tar", "v0.3.0/k0s-airgap.tar", "v0.3.0/new.tar"}
	if !slices.Equal(files, want) {
		t.Fatalf("retained %v, want %v", files, want)
	}
}
