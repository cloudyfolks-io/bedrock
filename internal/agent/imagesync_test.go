package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	sigyaml "sigs.k8s.io/yaml"
)

const (
	imageOne = "quay.io/a/one:1@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imageTwo = "quay.io/a/two:2@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	pauseRef = "docker.io/k0sproject/pause:3.10"
)

type fakeStore struct {
	root      string
	held      map[string]bool
	pinned    map[string]bool
	contents  map[string][]string
	pinCalls  []string
	linked    []string
	pinErr    error
	neverLoad bool
}

func (f *fakeStore) List(context.Context) ([]storedImage, error) {
	if !f.neverLoad {
		f.importLinked()
	}
	var images []storedImage
	for name := range f.held {
		images = append(images, storedImage{Name: name, Pinned: f.pinned[name]})
	}
	return images, nil
}

func (f *fakeStore) importLinked() {
	entries, _ := os.ReadDir(filepath.Join(f.root, k0sImagesDir))
	for _, entry := range entries {
		if !slices.Contains(f.linked, entry.Name()) {
			f.linked = append(f.linked, entry.Name())
		}
		for _, name := range f.contents[entry.Name()] {
			f.held[name] = true
		}
	}
}

func (f *fakeStore) Pin(_ context.Context, name string) error {
	if f.pinErr != nil {
		return f.pinErr
	}
	f.pinCalls = append(f.pinCalls, name)
	f.pinned[name] = true
	return nil
}

type syncFixture struct {
	deps  Deps
	store *fakeStore
}

func newSyncFixture(t *testing.T) syncFixture {
	t.Helper()
	root := t.TempDir()
	oneFile, twoFile := release.ImageFileName(imageOne), release.ImageFileName(imageTwo)
	spec, err := sigyaml.Marshal(release.BundleSpec{Version: "v0.3.0", Images: []release.BundleImage{{Ref: imageOne, File: oneFile}, {Ref: imageTwo, File: twoFile}}})
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, archiveFile(root, "v0.3.0", release.BundleFileName), string(spec))
	writeFixtureFile(t, archiveFile(root, "v0.3.0", oneFile), "one")
	writeFixtureFile(t, archiveFile(root, "v0.3.0", twoFile), "two")
	writeFixtureFile(t, archiveFile(root, "v0.3.0", airgapFile), string(tarWith(t, "index.json", `{"manifests":[{"annotations":{"io.containerd.image.name":"`+pauseRef+`"}}]}`)))
	oneNames, _ := release.ContainerdNames(imageOne)
	twoNames, _ := release.ContainerdNames(imageTwo)
	deps := newDeps(nil, time.Now())
	deps.Root = root
	deps.K0sTimeout = 200 * time.Millisecond
	deps.K0sPoll = 5 * time.Millisecond
	store := &fakeStore{root: root, held: map[string]bool{}, pinned: map[string]bool{}, contents: map[string][]string{
		oneFile: oneNames, twoFile: twoNames, versionedAirgap("v0.3.0"): {pauseRef},
	}}
	return syncFixture{deps: deps, store: store}
}

func tarWith(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (f syncFixture) holdAll() {
	for _, names := range f.store.contents {
		for _, name := range names {
			f.store.held[name] = true
		}
	}
}

func k0sDirEntries(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, k0sImagesDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestSyncPinsHeldImagesAndLinksNothing(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	if err := syncImages(context.Background(), fixture.deps, fixture.store); err != nil {
		t.Fatal(err)
	}
	if len(k0sDirEntries(t, fixture.deps.Root)) != 0 {
		t.Fatal("nothing may be linked when containerd holds every image")
	}
	oneNames, _ := release.ContainerdNames(imageOne)
	for _, name := range append(oneNames, pauseRef) {
		if !fixture.store.pinned[name] {
			t.Fatalf("%s must be pinned", name)
		}
	}
}

func TestSyncDoesNotPinAPinnedImageAgain(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	for name := range fixture.store.held {
		fixture.store.pinned[name] = true
	}
	if err := syncImages(context.Background(), fixture.deps, fixture.store); err != nil {
		t.Fatal(err)
	}
	if len(fixture.store.pinCalls) != 0 {
		t.Fatalf("pinned images must be left alone, pinned %v", fixture.store.pinCalls)
	}
}

func TestSyncLinksMissingTarballsWaitsPinsAndRemovesTheLinks(t *testing.T) {
	fixture := newSyncFixture(t)
	for _, name := range append(namesOf(t, imageOne), pauseRef) {
		fixture.store.held[name] = true
	}
	if err := syncImages(context.Background(), fixture.deps, fixture.store); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fixture.store.linked, []string{release.ImageFileName(imageTwo)}) {
		t.Fatalf("only the missing tarball may be linked, k0s saw %v", fixture.store.linked)
	}
	for _, name := range namesOf(t, imageTwo) {
		if !fixture.store.pinned[name] {
			t.Fatalf("%s must be pinned after the import", name)
		}
	}
	if len(k0sDirEntries(t, fixture.deps.Root)) != 0 {
		t.Fatalf("links must go after the pin: %v", k0sDirEntries(t, fixture.deps.Root))
	}
	if readFixtureFile(t, archiveFile(fixture.deps.Root, "v0.3.0", release.ImageFileName(imageTwo))) != "two" {
		t.Fatal("the archive must keep the tarball")
	}
}

func TestSyncAfterACrashBetweenLinkAndPinConverges(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	twoFile := release.ImageFileName(imageTwo)
	if err := os.MkdirAll(filepath.Join(fixture.deps.Root, k0sImagesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(archiveFile(fixture.deps.Root, "v0.3.0", twoFile), filepath.Join(fixture.deps.Root, k0sImagesDir, twoFile)); err != nil {
		t.Fatal(err)
	}
	if err := syncImages(context.Background(), fixture.deps, fixture.store); err != nil {
		t.Fatal(err)
	}
	if len(k0sDirEntries(t, fixture.deps.Root)) != 0 {
		t.Fatal("a leftover link of a held image must be removed")
	}
	twoNames, _ := release.ContainerdNames(imageTwo)
	for _, name := range twoNames {
		if !fixture.store.pinned[name] {
			t.Fatalf("%s must be pinned", name)
		}
	}
}

func TestSyncKeepsAForeignFileInTheK0sDirectory(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	twoFile := release.ImageFileName(imageTwo)
	writeFixtureFile(t, filepath.Join(fixture.deps.Root, k0sImagesDir, twoFile), "someone else")
	if err := syncImages(context.Background(), fixture.deps, fixture.store); err != nil {
		t.Fatal(err)
	}
	if readFixtureFile(t, filepath.Join(fixture.deps.Root, k0sImagesDir, twoFile)) != "someone else" {
		t.Fatal("only a link to the archived tarball may be removed")
	}
}

func TestSyncReportsAnImportThatNeverFinishes(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.store.neverLoad = true
	err := syncImages(context.Background(), fixture.deps, fixture.store)
	if err == nil || !strings.Contains(err.Error(), "is not imported") {
		t.Fatalf("error %v must name a missing image", err)
	}
	if len(k0sDirEntries(t, fixture.deps.Root)) == 0 {
		t.Fatal("links must stay so k0s can finish the import")
	}
	if len(fixture.store.pinCalls) != 0 {
		t.Fatal("nothing may be pinned before the import ends")
	}
}

func TestSyncReturnsAPinFailure(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	fixture.store.pinErr = errors.New("containerd is down")
	if err := syncImages(context.Background(), fixture.deps, fixture.store); err == nil || !strings.Contains(err.Error(), "containerd is down") {
		t.Fatalf("error %v", err)
	}
}

func TestSyncWithoutAnArchiveDoesNothing(t *testing.T) {
	deps := newDeps(nil, time.Now())
	deps.Root = t.TempDir()
	if err := syncImages(context.Background(), deps, &fakeStore{}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncUsesTarballIndexForTarballsWithoutAManifest(t *testing.T) {
	fixture := newSyncFixture(t)
	if err := os.Remove(archiveFile(fixture.deps.Root, "v0.3.0", release.BundleFileName)); err != nil {
		t.Fatal(err)
	}
	fixture.store.held[pauseRef] = true
	fixture.store.contents = map[string][]string{}
	fixture.store.neverLoad = true
	_ = syncImages(context.Background(), fixture.deps, fixture.store)
	if !fixture.store.pinned[pauseRef] {
		t.Fatal("names read from the airgap index must be pinned")
	}
	if entries := k0sDirEntries(t, fixture.deps.Root); len(entries) != 0 {
		t.Fatalf("tarballs with unknown names must not be linked: %v", entries)
	}
}

func TestTarballImageNamesReadsDockerManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.tar")
	writeFixtureFile(t, path, string(tarWith(t, "manifest.json", `[{"RepoTags":["registry.k8s.io/pause:3.10"]}]`)))
	names, err := tarballImageNames(path)
	if err != nil || !slices.Equal(names, []string{"registry.k8s.io/pause:3.10"}) {
		t.Fatalf("names %v err %v", names, err)
	}
}

func TestCtrStoreListParsesPinsFromTheLabelColumn(t *testing.T) {
	exec := &host.FakeExec{Responses: map[string]string{
		"/usr/local/bin/k0s ctr --namespace k8s.io images ls": "REF TYPE DIGEST SIZE PLATFORMS LABELS\n" +
			"a/b:1 application/vnd.oci.image.index.v1+json sha256:aa 1.2 MiB linux/amd64 io.cri-containerd.pinned=pinned,x=y\n" +
			"c/d:2 application/vnd.oci.image.index.v1+json sha256:bb 1.2 MiB linux/amd64 -\n",
	}}
	images, err := ctrStore{exec: exec}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []storedImage{{Name: "a/b:1", Pinned: true}, {Name: "c/d:2"}}
	if !slices.Equal(images, want) {
		t.Fatalf("images %v, want %v", images, want)
	}
}

func TestCtrStorePinSetsOurLabelAndClearsTheK0sLabel(t *testing.T) {
	exec := &host.FakeExec{ResponsePrefixes: map[string]string{"/usr/local/bin/k0s ctr": ""}}
	if err := (ctrStore{exec: exec}).Pin(context.Background(), "a/b:1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/local/bin/k0s ctr --namespace k8s.io images label a/b:1 io.k0sproject.ocibundle-paths=",
		"/usr/local/bin/k0s ctr --namespace k8s.io images label a/b:1 io.cri-containerd.pinned=pinned",
	}
	if !slices.Equal(exec.Calls, want) {
		t.Fatalf("calls %v, want %v", exec.Calls, want)
	}
}
