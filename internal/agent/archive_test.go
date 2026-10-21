package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func archiveFile(root, version, name string) string {
	return filepath.Join(root, archiveDir, version, runtime.GOARCH, name)
}

func TestAdoptMovesK0sImagesIntoTheArchive(t *testing.T) {
	root := t.TempDir()
	writeImageTarball(t, root, "a.tar")
	writeImageTarball(t, root, "k0s-airgap.tar")
	writeImageTarball(t, root, "k0s-airgap-v0.2.0.tar")
	if err := adoptK0sImages(root, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{archiveFile(root, "v0.3.0", "a.tar"), archiveFile(root, "v0.3.0", "k0s-airgap.tar"), archiveFile(root, "v0.2.0", "k0s-airgap.tar")} {
		if readFixtureFile(t, kept) != "tar" {
			t.Fatalf("%s must hold the moved tarball", kept)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, k0sImagesDir))
	if len(entries) != 0 {
		t.Fatalf("the k0s images directory must be empty, left %v", entries)
	}
}

func TestAdoptKeepsAnArchivedTarballAndIsRepeatable(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, archiveFile(root, "v0.3.0", "a.tar"), "archived")
	writeImageTarball(t, root, "a.tar")
	for range 2 {
		if err := adoptK0sImages(root, "v0.3.0"); err != nil {
			t.Fatal(err)
		}
	}
	if got := readFixtureFile(t, archiveFile(root, "v0.3.0", "a.tar")); got != "archived" {
		t.Fatalf("an archived tarball must stay, got %q", got)
	}
	if exists(filepath.Join(root, k0sImagesDir, "a.tar")) {
		t.Fatal("the duplicate in the k0s directory must go")
	}
}

func TestAdoptWithoutTheK0sDirectoryDoesNothing(t *testing.T) {
	if err := adoptK0sImages(t.TempDir(), "v0.3.0"); err != nil {
		t.Fatal(err)
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
