package agent

import (
	"os"
	"path/filepath"
	"testing"

	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestInitBundleSpecLetsTheAgentArchiveEveryPreloadedTarball(t *testing.T) {
	root := t.TempDir()
	bundleDir := t.TempDir()
	spec, err := sigyaml.Marshal(release.BundleSpec{Version: "dev", Images: []release.BundleImage{{File: "a.tar"}, {File: "b.tar"}}})
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(bundleDir, release.BundleFileName), string(spec))
	writeFixtureFile(t, filepath.Join(bundleDir, bundleImagesDir, airgapFile), "tar")
	for _, name := range []string{airgapFile, "a.tar", "b.tar"} {
		writeImageTarball(t, root, name)
	}
	if err := ArchiveInitBundle(root, bundleDir); err != nil {
		t.Fatal(err)
	}
	if err := adoptK0sImages(root, releaseLists(t.Context(), nil, root), ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{airgapFile, "a.tar", "b.tar", release.BundleFileName} {
		if !exists(archiveFile(root, "dev", name)) {
			t.Fatalf("%s must be archived", name)
		}
	}
	if left := k0sDirEntries(t, root); len(left) != 0 {
		t.Fatalf("k0s directory must be empty, left %v", left)
	}
}

func TestArchiveInitBundleKeepsTheBundleSpecAfterTheBundleDirIsRemoved(t *testing.T) {
	root := t.TempDir()
	bundleDir := t.TempDir()
	writeFixtureFile(t, filepath.Join(bundleDir, release.BundleFileName), "version: dev\n")
	writeFixtureFile(t, filepath.Join(bundleDir, bundleImagesDir, airgapFile), "tar")
	if err := ArchiveInitBundle(root, bundleDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(bundleDir); err != nil {
		t.Fatal(err)
	}
	if readFixtureFile(t, archiveFile(root, "dev", release.BundleFileName)) != "version: dev\n" {
		t.Fatal("archived bundle spec lost")
	}
}
