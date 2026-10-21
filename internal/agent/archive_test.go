package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func archiveFile(root, version, name string) string {
	return filepath.Join(root, archiveDir, version, runtime.GOARCH, name)
}

func k0sFile(root, name string) string {
	return filepath.Join(root, k0sImagesDir, name)
}

var twoVersionLists = map[string][]string{
	"v0.2.0": {"shared.tar", "old.tar"},
	"v0.3.0": {"shared.tar", "new.tar"},
}

func TestAdoptFilesATarballIntoEveryVersionThatNamesIt(t *testing.T) {
	root := t.TempDir()
	writeImageTarball(t, root, "shared.tar")
	writeImageTarball(t, root, "new.tar")
	if err := adoptK0sImages(root, twoVersionLists, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{archiveFile(root, "v0.2.0", "shared.tar"), archiveFile(root, "v0.3.0", "shared.tar"), archiveFile(root, "v0.3.0", "new.tar")} {
		if readFixtureFile(t, kept) != "tar" {
			t.Fatalf("%s must hold the tarball", kept)
		}
	}
	if exists(archiveFile(root, "v0.2.0", "new.tar")) {
		t.Fatal("a version that does not name the tarball must not get it")
	}
	if len(k0sDirEntries(t, root)) != 0 {
		t.Fatalf("filed tarballs must leave the k0s directory, left %v", k0sDirEntries(t, root))
	}
}

func TestAdoptLeavesATarballThatNoVersionNamesUntouched(t *testing.T) {
	root := t.TempDir()
	writeImageTarball(t, root, "stranger.tar")
	if err := adoptK0sImages(root, twoVersionLists, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	if readFixtureFile(t, k0sFile(root, "stranger.tar")) != "tar" {
		t.Fatal("an unnamed tarball must stay in the k0s directory")
	}
}

func TestAdoptFilesAirgapTarballsByVersion(t *testing.T) {
	root := t.TempDir()
	writeImageTarball(t, root, "k0s-airgap.tar")
	writeImageTarball(t, root, "k0s-airgap-v0.3.0.tar")
	if err := adoptK0sImages(root, nil, "v0.2.0"); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"v0.2.0", "v0.3.0"} {
		if readFixtureFile(t, archiveFile(root, version, "k0s-airgap.tar")) != "tar" {
			t.Fatalf("%s must hold its airgap tarball", version)
		}
	}
	if len(k0sDirEntries(t, root)) != 0 {
		t.Fatalf("left %v", k0sDirEntries(t, root))
	}
}

func TestAdoptNeverDeletesASourceThatDiffersFromTheArchivedFile(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, archiveFile(root, "v0.3.0", "k0s-airgap.tar"), "other content")
	writeImageTarball(t, root, "k0s-airgap.tar")
	if err := adoptK0sImages(root, nil, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	if readFixtureFile(t, k0sFile(root, "k0s-airgap.tar")) != "tar" {
		t.Fatal("a source that differs from the archived file must stay")
	}
	if readFixtureFile(t, archiveFile(root, "v0.3.0", "k0s-airgap.tar")) != "other content" {
		t.Fatal("the archived file must stay")
	}
}

func TestAdoptRemovesASourceEqualToTheArchivedFile(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, archiveFile(root, "v0.3.0", "new.tar"), "tar")
	writeImageTarball(t, root, "new.tar")
	for range 2 {
		if err := adoptK0sImages(root, twoVersionLists, "v0.3.0"); err != nil {
			t.Fatal(err)
		}
	}
	if len(k0sDirEntries(t, root)) != 0 {
		t.Fatal("a duplicate must go")
	}
}

func TestAdoptSkipsLinksTheAgentMade(t *testing.T) {
	root := t.TempDir()
	writeArchived(t, root, "v0.2.0", "old.tar")
	writeImageTarball(t, root, "v0.2.0-old.tar")
	if err := recordLinks(root, []string{"v0.2.0-old.tar"}); err != nil {
		t.Fatal(err)
	}
	if err := adoptK0sImages(root, twoVersionLists, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	if !exists(k0sFile(root, "v0.2.0-old.tar")) || exists(archiveFile(root, "v0.3.0", "v0.2.0-old.tar")) {
		t.Fatal("a recorded link is not a legacy file")
	}
}

func TestAdoptSkipsAFileThatIsTheSameInodeAsAnArchivedFile(t *testing.T) {
	root := t.TempDir()
	writeArchived(t, root, "v0.2.0", "old.tar")
	if err := os.MkdirAll(filepath.Join(root, k0sImagesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(archiveFile(root, "v0.2.0", "old.tar"), k0sFile(root, "restore-script-name.tar")); err != nil {
		t.Fatal(err)
	}
	if err := adoptK0sImages(root, twoVersionLists, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	if !exists(k0sFile(root, "restore-script-name.tar")) {
		t.Fatal("a hard link of an archived file must not be filed again")
	}
}

func TestAdoptWithoutTheK0sDirectoryDoesNothing(t *testing.T) {
	if err := adoptK0sImages(t.TempDir(), twoVersionLists, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseListsReadsBundleManifestsOfTheDepotAndTheArchive(t *testing.T) {
	root := t.TempDir()
	spec, err := sigyaml.Marshal(release.BundleSpec{Version: "v0.3.0", Images: []release.BundleImage{{Ref: pinnedImage, File: release.ImageFileName(pinnedImage)}}})
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/depot/v0.3.0", runtime.GOARCH, release.BundleFileName), string(spec))
	got := releaseLists(t.Context(), nil, root)
	if !slices.Equal(got["v0.3.0"], []string{release.ImageFileName(pinnedImage)}) {
		t.Fatalf("lists %v", got)
	}
}

func TestRetainArchiveCarriesSharedTarballsAndDropsOtherVersions(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, archiveFile(root, "v0.2.0", "shared.tar"), "shared")
	writeFixtureFile(t, archiveFile(root, "v0.2.0", "old.tar"), "old")
	writeFixtureFile(t, archiveFile(root, "v0.3.0", "new.tar"), "new")
	if err := retainArchive(root, "v0.3.0", []string{"shared.tar", "new.tar"}); err != nil {
		t.Fatal(err)
	}
	if readFixtureFile(t, archiveFile(root, "v0.3.0", "shared.tar")) != "shared" || readFixtureFile(t, archiveFile(root, "v0.3.0", "new.tar")) != "new" {
		t.Fatal("the kept version must hold its own and the shared tarballs")
	}
	if exists(filepath.Join(root, archiveDir, "v0.2.0")) {
		t.Fatal("the other version must be removed")
	}
}

func TestRetainArchiveRemovesFilesTheKeptListDoesNotName(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, archiveFile(root, "v0.3.0", "new.tar"), "new")
	writeFixtureFile(t, archiveFile(root, "v0.3.0", "stale.tar"), "stale")
	writeFixtureFile(t, archiveFile(root, "v0.3.0", "k0s-airgap.tar"), "airgap")
	writeFixtureFile(t, archiveFile(root, "v0.3.0", release.BundleFileName), "manifest")
	if err := retainArchive(root, "v0.3.0", archiveFileNames([]string{pinnedImage})); err != nil {
		t.Fatal(err)
	}
	if exists(archiveFile(root, "v0.3.0", "stale.tar")) || exists(archiveFile(root, "v0.3.0", "new.tar")) {
		t.Fatal("files outside the list must go")
	}
	if !exists(archiveFile(root, "v0.3.0", "k0s-airgap.tar")) || !exists(archiveFile(root, "v0.3.0", release.BundleFileName)) {
		t.Fatal("the airgap tarball and the manifest must stay")
	}
}

func TestRetainArchiveDoesNotCarryAnAirgapTarballAcrossVersions(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, archiveFile(root, "v0.2.0", "k0s-airgap.tar"), "airgap of v0.2.0")
	writeFixtureFile(t, archiveFile(root, "v0.3.0", "new.tar"), "new")
	if err := retainArchive(root, "v0.3.0", archiveFileNames(nil)); err != nil {
		t.Fatal(err)
	}
	if exists(archiveFile(root, "v0.3.0", "k0s-airgap.tar")) {
		t.Fatal("an airgap tarball belongs to one version")
	}
}

func TestRetainArchiveKeepsEverythingWhenTheKeptVersionIsMissing(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, archiveFile(root, "v0.2.0", "old.tar"), "old")
	if err := retainArchive(root, "v0.3.0", []string{"new.tar"}); err != nil {
		t.Fatal(err)
	}
	if !exists(archiveFile(root, "v0.2.0", "old.tar")) {
		t.Fatal("the last copy of a tarball must never go")
	}
}

func TestRetainArchiveWithoutAnArchiveDoesNothing(t *testing.T) {
	if err := retainArchive(t.TempDir(), "v0.3.0", nil); err != nil {
		t.Fatal(err)
	}
}
