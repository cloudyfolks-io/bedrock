package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
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
	writeImageTarball(t, root, "k0s-airgap-v0.3.0.tar")
	writeFixtureFile(t, filepath.Join(root, "var/lib/k0s/.pre-restore-20261001T110000Z/etcd/data"), "old etcd")
	if _, err := cleanup(context.Background(), env); err == nil || !strings.Contains(err.Error(), "containerd is down") {
		t.Fatalf("error %v", err)
	}
	for _, path := range []string{"var/lib/k0s/images/k0s-airgap-v0.3.0.tar", "var/lib/bedrock/staged/v0.3.0", "var/lib/bedrock/previous/k0s", "var/lib/k0s/.pre-restore-20261001T110000Z"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed although the image removal failed", path)
		}
	}
}

func writeImageTarball(t *testing.T, root, name string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(root, k0sImagesDir, name), "tar")
}

func TestPruneRemovesOldImageTarballsAndAirgapKeepsTarget(t *testing.T) {
	env, _ := finishEnv(t, nil)
	root := env.Deps.Root
	writeImageTarball(t, root, release.ImageFileName(oldOnly))
	writeImageTarball(t, root, release.ImageFileName(shared))
	writeImageTarball(t, root, release.ImageFileName(newOnly))
	writeImageTarball(t, root, release.ImageFileName(retagged))
	writeImageTarball(t, root, "k0s-airgap.tar")
	writeImageTarball(t, root, "k0s-airgap-v0.2.0.tar")
	writeImageTarball(t, root, "k0s-airgap-v0.3.0.tar")
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{release.ImageFileName(oldOnly), release.ImageFileName(shared), "k0s-airgap.tar", "k0s-airgap-v0.2.0.tar"} {
		if _, err := os.Stat(filepath.Join(root, k0sImagesDir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed", gone)
		}
	}
	for _, kept := range []string{release.ImageFileName(newOnly), release.ImageFileName(retagged), "k0s-airgap-v0.3.0.tar"} {
		if _, err := os.Stat(filepath.Join(root, k0sImagesDir, kept)); err != nil {
			t.Fatalf("%s must be kept: %v", kept, err)
		}
	}
}

func TestCleanupRemovesTargetOnlyTarballsKeepsFromAndBaseAirgap(t *testing.T) {
	env, _ := finishEnv(t, nil)
	root := env.Deps.Root
	writeImageTarball(t, root, release.ImageFileName(oldOnly))
	writeImageTarball(t, root, release.ImageFileName(shared))
	writeImageTarball(t, root, release.ImageFileName(newOnly))
	writeImageTarball(t, root, release.ImageFileName(retagged))
	writeImageTarball(t, root, "k0s-airgap.tar")
	writeImageTarball(t, root, "k0s-airgap-v0.3.0.tar")
	if _, err := cleanup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{release.ImageFileName(newOnly), release.ImageFileName(retagged), "k0s-airgap-v0.3.0.tar"} {
		if _, err := os.Stat(filepath.Join(root, k0sImagesDir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed", gone)
		}
	}
	for _, kept := range []string{release.ImageFileName(oldOnly), release.ImageFileName(shared), "k0s-airgap.tar"} {
		if _, err := os.Stat(filepath.Join(root, k0sImagesDir, kept)); err != nil {
			t.Fatalf("%s must be kept: %v", kept, err)
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

func restoreEnv(t *testing.T, exec *host.FakeExec) StepEnv {
	t.Helper()
	root := t.TempDir()
	work := t.TempDir()
	writeFixtureFile(t, filepath.Join(work, "k0s_backup_2026-10-01T10_00_00Z.tar.gz"), "etcd snapshot")
	writeFixtureFile(t, filepath.Join(work, "ovn", "ovnnb_db.db"), "nb")
	archive := filepath.Join(root, "var/lib/bedrock/backups/bedrock-v0.2.0-20261001T100000Z.tar.gz")
	if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeArchive(work, archive); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/previous/k0s"), "old k0s")
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s"), "new k0s")
	for _, dir := range []string{"etcd", "pki", "manifests", "images", "containerd"} {
		writeFixtureFile(t, filepath.Join(root, "var/lib/k0s", dir, "data"), dir)
	}
	deps := newDeps(exec, time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC))
	deps.Root = root
	return StepEnv{Deps: deps, Upgrade: v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v0.3.0", From: "v0.2.0", Attempt: 1, Backup: "/var/lib/bedrock/backups/bedrock-v0.2.0-20261001T100000Z.tar.gz"}}}
}

func TestRestoreRollsBackK0sAndEtcd(t *testing.T) {
	exec := &host.FakeExec{Responses: map[string]string{"systemctl stop k0scontroller.service": "", "systemctl start k0scontroller.service": ""}, ResponsePrefixes: map[string]string{"/usr/local/bin/k0s restore --config-out ": ""}}
	env := restoreEnv(t, exec)
	outcome, err := restore(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	root := env.Deps.Root
	if readFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s")) != "old k0s" {
		t.Fatal("the previous k0s binary must be back")
	}
	if len(exec.Calls) != 3 || exec.Calls[0] != "systemctl stop k0scontroller.service" || !strings.HasPrefix(exec.Calls[1], "/usr/local/bin/k0s restore --config-out ") || !strings.HasSuffix(exec.Calls[1], "k0s_backup_2026-10-01T10_00_00Z.tar.gz") || exec.Calls[2] != "systemctl start k0scontroller.service" {
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
	if outcome.Message != "restored "+env.Upgrade.Spec.Backup {
		t.Fatalf("message %q", outcome.Message)
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
