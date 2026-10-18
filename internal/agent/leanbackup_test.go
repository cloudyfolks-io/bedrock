package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
)

const k0sArchiveName = "k0s_backup_2026-10-01T10_00_00Z.tar.gz"

type tarFixture struct {
	name string
	body string
}

var k0sFixture = []tarFixture{
	{"etcd-snapshot.db", "etcd snapshot"},
	{"pki/ca.crt", "certificate"},
	{"manifests/stack/app.yaml", "manifest"},
	{"images/", ""},
	{"images/bundle.tar", "image bundle"},
	{"images/nested/layer.tar", "image layer"},
	{"imagestore/keep.txt", "a name that only starts with images"},
	{"k0s.yaml", "config"},
}

var k0sFixtureWithoutImages = []tarFixture{
	{"etcd-snapshot.db", "etcd snapshot"},
	{"pki/ca.crt", "certificate"},
	{"manifests/stack/app.yaml", "manifest"},
	{"imagestore/keep.txt", "a name that only starts with images"},
	{"k0s.yaml", "config"},
}

func gzipTar(entries []tarFixture) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	writer := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o640, ModTime: time.Unix(1_700_000_000, 0), Typeflag: tar.TypeReg, Size: int64(len(entry.body))}
		if strings.HasSuffix(entry.name, "/") {
			header.Typeflag, header.Mode, header.Size = tar.TypeDir, 0o750, 0
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := writer.Write([]byte(entry.body)); err != nil {
			return nil, err
		}
	}
	if err := errors.Join(writer.Close(), gz.Close()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func readGzipTar(t *testing.T, raw []byte) []tarFixture {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)
	var entries []tarFixture
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, tarFixture{header.Name, string(body)})
	}
}

func sealedFiles(t *testing.T, path string, identity age.Identity) map[string][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	plain, err := seal.Open(file, []age.Identity{identity})
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)
	files := make(map[string][]byte)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if files[header.Name], err = io.ReadAll(reader); err != nil {
			t.Fatal(err)
		}
	}
}

func requireTar(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar is not installed")
	}
}

func tarList(t *testing.T, archive string) []string {
	t.Helper()
	out, err := exec.Command("tar", "-tzf", archive).CombinedOutput()
	if err != nil {
		t.Fatalf("tar -tz: %v\n%s", err, out)
	}
	return strings.Fields(string(out))
}

func noisyFixture(t *testing.T) []tarFixture {
	t.Helper()
	noise := make([]byte, 256<<10)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	return []tarFixture{
		{"etcd-snapshot.db", "etcd snapshot"},
		{"images/bundle.tar", string(noise)},
		{"k0s.yaml", "config"},
	}
}

func truncateArchive(work string) error {
	path := filepath.Join(work, k0sArchiveName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw[:len(raw)/2], 0o600)
}

func TestEtcdSnapshotLeavesImagesOut(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	deps := snapshotDeps(t, &fakeK0sBackup{}, at("2026-10-08T02:30:00Z"))
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	files := sealedFiles(t, filepath.Join(deps.Root, backupDir, "etcd-20261008T023000Z.tar.gz.age"), hostIdentity(t, deps.Root))
	if got := readGzipTar(t, files[k0sArchiveName]); !slices.Equal(got, k0sFixtureWithoutImages) {
		t.Fatalf("k0s archive entries %v, want %v", got, k0sFixtureWithoutImages)
	}
}

func TestUpgradeBackupKeepsImages(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{})
	if _, err := backup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	files := sealedFiles(t, filepath.Join(env.Deps.Root, backupDir, "bedrock-v0.2.0-20261001T100000Z.tar.gz.age"), hostIdentity(t, env.Deps.Root))
	want, err := gzipTar(k0sFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(files[k0sArchiveName], want) {
		t.Fatalf("an upgrade backup must hold the k0s archive as k0s wrote it: %v", readGzipTar(t, files[k0sArchiveName]))
	}
}

func TestEtcdSnapshotExtractsWithTar(t *testing.T) {
	requireTar(t)
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	deps := snapshotDeps(t, &fakeK0sBackup{}, at("2026-10-08T02:30:00Z"))
	writeFixtureFile(t, filepath.Join(deps.Root, "etc/origin/ovn/ovnnb_db.db"), "nb")
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(deps.Root, backupDir, "etcd-20261008T023000Z.tar.gz.age"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	plain, err := seal.Open(file, []age.Identity{hostIdentity(t, deps.Root)})
	if err != nil {
		t.Fatal(err)
	}
	extracted := t.TempDir()
	untar := exec.Command("tar", "-xz", "-C", extracted)
	untar.Stdin = plain
	if out, err := untar.CombinedOutput(); err != nil {
		t.Fatalf("tar -xz: %v\n%s", err, out)
	}
	if got := readFixtureFile(t, filepath.Join(extracted, "ovn/ovnnb_db.db")); got != "nb" {
		t.Fatalf("ovn database %q", got)
	}
	want := []string{"etcd-snapshot.db", "pki/ca.crt", "manifests/stack/app.yaml", "imagestore/keep.txt", "k0s.yaml"}
	if got := tarList(t, filepath.Join(extracted, k0sArchiveName)); !slices.Equal(got, want) {
		t.Fatalf("tar -tz lists %v, want %v", got, want)
	}
}

func TestEtcdSnapshotFailingInTheFilterLeavesNothingToCount(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	deps := snapshotDeps(t, &fakeK0sBackup{}, at("2026-10-08T02:30:00Z"))
	deps.Exec = &brokenWorkBackup{fakeK0sBackup: fakeK0sBackup{entries: noisyFixture(t)}, damage: truncateArchive}
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err == nil {
		t.Fatal("a snapshot whose k0s archive cannot be filtered must fail")
	}
	if got := snapshotFiles(t, deps, "*"); !slices.Equal(got, []string{snapshotFailureFile}) {
		t.Fatalf("files %v", got)
	}
	if stamp := newestEtcdSnapshot(filepath.Join(deps.Root, backupDir)); !stamp.IsZero() {
		t.Fatalf("a failed snapshot counts as taken at %s", stamp)
	}
	deps.Exec = &fakeK0sBackup{}
	deps.Now = func() time.Time { return at("2026-10-08T02:32:00Z") }
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFiles(t, deps, "*"); !slices.Equal(got, []string{"etcd-20261008T023200Z.tar.gz.age"}) {
		t.Fatalf("files %v", got)
	}
}

func TestDropImagesKeepsTheOriginalWhenTheArchiveIsTruncated(t *testing.T) {
	full, err := gzipTar(noisyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	truncated := full[:len(full)/2]
	work := t.TempDir()
	writeFixtureFile(t, filepath.Join(work, k0sArchiveName), string(truncated))
	if err := dropImages(work); err == nil {
		t.Fatal("a truncated archive must fail")
	}
	if got := listNames(t, work); !slices.Equal(got, []string{k0sArchiveName}) {
		t.Fatalf("files %v", got)
	}
	if got := readFixtureFile(t, filepath.Join(work, k0sArchiveName)); got != string(truncated) {
		t.Fatal("a failed filter must not change the original")
	}
}

func TestDropImagesFailsOnAWrongChecksum(t *testing.T) {
	full, err := gzipTar(k0sFixture)
	if err != nil {
		t.Fatal(err)
	}
	full[len(full)-8] ^= 0xff
	work := t.TempDir()
	writeFixtureFile(t, filepath.Join(work, k0sArchiveName), string(full))
	if err := dropImages(work); err == nil {
		t.Fatal("an archive with a wrong checksum must fail")
	}
	if got := listNames(t, work); !slices.Equal(got, []string{k0sArchiveName}) {
		t.Fatalf("files %v", got)
	}
}

func TestDropImagesFailsWithoutAnArchive(t *testing.T) {
	if err := dropImages(t.TempDir()); !errors.Is(err, errNoK0sBackup) {
		t.Fatalf("error %v, want %v", err, errNoK0sBackup)
	}
}

func TestDropImagesWritesAValidTarGz(t *testing.T) {
	requireTar(t)
	raw, err := gzipTar(k0sFixture)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	writeFixtureFile(t, filepath.Join(work, k0sArchiveName), string(raw))
	if err := dropImages(work); err != nil {
		t.Fatal(err)
	}
	if got := listNames(t, work); !slices.Equal(got, []string{k0sArchiveName}) {
		t.Fatalf("files %v", got)
	}
	want := []string{"etcd-snapshot.db", "pki/ca.crt", "manifests/stack/app.yaml", "imagestore/keep.txt", "k0s.yaml"}
	if got := tarList(t, filepath.Join(work, k0sArchiveName)); !slices.Equal(got, want) {
		t.Fatalf("tar -tz lists %v, want %v", got, want)
	}
}

func TestIsImagesEntry(t *testing.T) {
	cases := map[string]bool{
		"images":             true,
		"images/":            true,
		"images/a.tar":       true,
		"./images/a.tar":     true,
		"images/dir/b.tar":   true,
		"imagestore/a.tar":   false,
		"pki/images/a.tar":   false,
		"images.yaml":        false,
		"etcd-snapshot.db":   false,
		"manifests/images/x": false,
	}
	for name, want := range cases {
		if got := isImagesEntry(name); got != want {
			t.Fatalf("%q: got %v, want %v", name, got, want)
		}
	}
}
