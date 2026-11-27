package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

const encryptionFixture = "apiVersion: apiserver.config.k8s.io/v1\nkind: EncryptionConfiguration\n"

func encryptionPath(root string) string {
	return filepath.Join(root, "etc/bedrock/encryption/encryption.yaml")
}

func controlPlaneOwn(name string) v1alpha1.Host {
	return hostWithRole(name, v1alpha1.RoleControlPlane)
}

func TestEncryptionStatusReportsTheHashOfTheFile(t *testing.T) {
	deps := authnDeps(t, time.Now())
	writeFixtureFile(t, encryptionPath(deps.Root), encryptionFixture)
	touch(t, encryptionPath(deps.Root), time.Now().Add(-2*time.Hour))
	fakeAPIServer(t, deps.Root, time.Now().Add(-time.Hour))

	got := syncEncryptionStatus(deps, controlPlaneOwn("node-a"))
	if got == nil || got.Hash != encryption.Hash([]byte(encryptionFixture)) || got.RestartPending || got.FailedHash != "" {
		t.Fatalf("status %+v", got)
	}
}

func TestEncryptionStatusFlagsAFileNewerThanTheAPIServer(t *testing.T) {
	deps := authnDeps(t, time.Now())
	writeFixtureFile(t, encryptionPath(deps.Root), encryptionFixture)
	fakeAPIServer(t, deps.Root, time.Now().Add(-time.Hour))
	if got := syncEncryptionStatus(deps, controlPlaneOwn("node-a")); got == nil || !got.RestartPending {
		t.Fatalf("status %+v", got)
	}
}

func TestEncryptionStatusKeepsTheOldAnswerWhenTheAPIServerIsUnknown(t *testing.T) {
	deps := authnDeps(t, time.Now())
	writeFixtureFile(t, encryptionPath(deps.Root), encryptionFixture)
	own := controlPlaneOwn("node-a")
	own.Status.Encryption = &v1alpha1.EncryptionFileStatus{RestartPending: true}
	if got := syncEncryptionStatus(deps, own); got == nil || !got.RestartPending {
		t.Fatalf("status %+v", got)
	}
}

func TestEncryptionStatusIsEmptyOnWorkersAndUnencryptedHosts(t *testing.T) {
	deps := authnDeps(t, time.Now())
	if got := syncEncryptionStatus(deps, controlPlaneOwn("node-a")); got != nil {
		t.Fatalf("no file: status %+v", got)
	}
	writeFixtureFile(t, encryptionPath(deps.Root), encryptionFixture)
	if got := syncEncryptionStatus(deps, hostWithRole("node-w", v1alpha1.RoleWorkload)); got != nil {
		t.Fatalf("worker: status %+v", got)
	}
}

func TestEncryptionStatusReportsTheFailedHash(t *testing.T) {
	deps := authnDeps(t, time.Now())
	writeFixtureFile(t, encryptionPath(deps.Root), encryptionFixture)
	writeFixtureFile(t, filepath.Join(deps.Root, "etc/bedrock/encryption/.apply-failed"), "abc123\n")
	fakeAPIServer(t, deps.Root, time.Now().Add(time.Hour))
	if got := syncEncryptionStatus(deps, controlPlaneOwn("node-a")); got == nil || got.FailedHash != "abc123" {
		t.Fatalf("status %+v", got)
	}
}

func TestTickReportsTheEncryptionFile(t *testing.T) {
	createHost(t, "node-enc", false, "")
	own := getHost(t, "node-enc")
	own.Spec.Roles = []string{v1alpha1.RoleControlPlane}
	if err := k8sClient.Update(context.Background(), &own); err != nil {
		t.Fatal(err)
	}
	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Node = "node-enc"
	deps.Root = t.TempDir()
	writeFixtureFile(t, encryptionPath(deps.Root), encryptionFixture)
	fakeAPIServer(t, deps.Root, time.Now().Add(time.Hour))
	_ = Tick(context.Background(), k8sClient, deps)
	got := getHost(t, "node-enc").Status.Encryption
	if got == nil || got.Hash != encryption.Hash([]byte(encryptionFixture)) {
		t.Fatalf("status %+v", got)
	}
}
