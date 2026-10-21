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

	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	imageOne = "quay.io/a/one:1@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imageTwo = "quay.io/a/two:2@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	pauseRef = "quay.io/k0sproject/pause:3.10.2-0"
)

type fakeStore struct {
	root          string
	held          map[string]bool
	pinned        map[string]bool
	sourceLabeled map[string]bool
	contents      map[string][]string
	pinCalls      []string
	unpinCalls    []string
	pinErr        error
	neverLoad     bool
}

func (f *fakeStore) List(context.Context) ([]storedImage, error) {
	if !f.neverLoad {
		f.importLinked()
	}
	var images []storedImage
	for name := range f.held {
		images = append(images, storedImage{Name: name, Pinned: f.pinned[name], SourceLabeled: f.sourceLabeled[name]})
	}
	return images, nil
}

func (f *fakeStore) importLinked() {
	entries, _ := os.ReadDir(filepath.Join(f.root, k0sImagesDir))
	for _, entry := range entries {
		for _, name := range f.contents[entry.Name()] {
			f.held[name] = true
			f.pinned[name] = true
			f.sourceLabeled[name] = true
		}
	}
}

func (f *fakeStore) Pin(_ context.Context, name string) error {
	if f.pinErr != nil {
		return f.pinErr
	}
	f.pinCalls = append(f.pinCalls, name)
	f.pinned[name] = true
	f.sourceLabeled[name] = false
	return nil
}

func (f *fakeStore) Unpin(_ context.Context, name string) error {
	f.unpinCalls = append(f.unpinCalls, name)
	f.pinned[name] = false
	return nil
}

type syncFixture struct {
	deps  Deps
	store *fakeStore
	clock *time.Time
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
	writeFixtureFile(t, archiveFile(root, "v0.3.0", airgapFile), string(k0sAirgapIndex(t)))
	oneNames, _ := release.ContainerdNames(imageOne)
	twoNames, _ := release.ContainerdNames(imageTwo)
	clock := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	deps := newDeps(nil, clock)
	deps.Now = func() time.Time { return clock }
	deps.Root = root
	deps.K0sTimeout = time.Minute
	store := &fakeStore{root: root, held: map[string]bool{}, pinned: map[string]bool{}, sourceLabeled: map[string]bool{}, contents: map[string][]string{
		"v0.3.0-" + oneFile: oneNames, "v0.3.0-" + twoFile: twoNames, "v0.3.0-" + airgapFile: {pauseRef},
	}}
	fixture := syncFixture{deps: deps, store: store, clock: &clock}
	fixture.deps.Now = func() time.Time { return *fixture.clock }
	return fixture
}

func k0sAirgapIndex(t *testing.T) []byte {
	t.Helper()
	return tarWith(t, "index.json", `{"schemaVersion":2,"manifests":[`+
		`{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:aa","size":1,"annotations":{"org.opencontainers.image.ref.name":"`+pauseRef+`"}},`+
		`{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:aa","size":1,"annotations":{"org.opencontainers.image.ref.name":"quay.io/k0sproject/pause@sha256:aa"}}]}`)
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
			f.store.pinned[name] = true
		}
	}
	f.store.held["quay.io/k0sproject/pause@sha256:aa"] = true
	f.store.pinned["quay.io/k0sproject/pause@sha256:aa"] = true
}

func (f syncFixture) sync(t *testing.T) error {
	t.Helper()
	return syncImages(context.Background(), f.deps, f.store)
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

func TestSyncLeavesSettledImagesAloneAndLinksNothing(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	if len(k0sDirEntries(t, fixture.deps.Root)) != 0 || len(fixture.store.pinCalls) != 0 {
		t.Fatalf("nothing to link, nothing to pin: pins %v", fixture.store.pinCalls)
	}
}

func TestSyncPinsAnImageThatKeepsTheK0sSourceLabel(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	fixture.store.sourceLabeled[pauseRef] = true
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fixture.store.pinCalls, []string{pauseRef}) {
		t.Fatalf("an image with the k0s label is not done: pins %v", fixture.store.pinCalls)
	}
}

func TestSyncPinsAnImageThatIsHeldButNotPinned(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	fixture.store.pinned[pauseRef] = false
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fixture.store.pinCalls, []string{pauseRef}) {
		t.Fatalf("pins %v", fixture.store.pinCalls)
	}
}

func TestSyncLinksMissingTarballsThenPinsAndUnlinksAfterTheImport(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	for _, name := range namesOf(t, imageTwo) {
		delete(fixture.store.held, name)
		delete(fixture.store.pinned, name)
	}
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	link := "v0.3.0-" + release.ImageFileName(imageTwo)
	if !slices.Equal(k0sDirEntries(t, fixture.deps.Root), []string{link}) || !slices.Equal(readLinkRecord(fixture.deps.Root), []string{link}) {
		t.Fatalf("only the missing tarball may be linked and recorded: %v %v", k0sDirEntries(t, fixture.deps.Root), readLinkRecord(fixture.deps.Root))
	}
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	for _, name := range namesOf(t, imageTwo) {
		if !fixture.store.pinned[name] || fixture.store.sourceLabeled[name] {
			t.Fatalf("%s must be pinned without the k0s label", name)
		}
	}
	if len(k0sDirEntries(t, fixture.deps.Root)) != 0 || len(readLinkRecord(fixture.deps.Root)) != 0 {
		t.Fatal("the link and its record must go after the pin")
	}
	if readFixtureFile(t, archiveFile(fixture.deps.Root, "v0.3.0", release.ImageFileName(imageTwo))) != "two" {
		t.Fatal("the archive must keep the tarball")
	}
}

func TestSyncPinsBeforeItRemovesTheLink(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	link := "v0.3.0-" + release.ImageFileName(imageTwo)
	writeFixtureFile(t, k0sFile(fixture.deps.Root, link), "two")
	if err := recordLinks(fixture.deps.Root, []string{link}); err != nil {
		t.Fatal(err)
	}
	for _, name := range namesOf(t, imageTwo) {
		fixture.store.sourceLabeled[name] = true
	}
	failing := &failingPin{fakeStore: fixture.store}
	if err := syncImages(context.Background(), fixture.deps, failing); err == nil {
		t.Fatal("a pin failure must be reported")
	}
	if !exists(k0sFile(fixture.deps.Root, link)) {
		t.Fatal("the link must stay while the image is not done")
	}
}

type failingPin struct{ *fakeStore }

func (failingPin) Pin(context.Context, string) error { return errors.New("containerd is down") }

func TestSyncAfterACrashBetweenLinkAndPinConverges(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	link := "v0.3.0-" + release.ImageFileName(imageTwo)
	writeFixtureFile(t, k0sFile(fixture.deps.Root, link), "two")
	if err := recordLinks(fixture.deps.Root, []string{link}); err != nil {
		t.Fatal(err)
	}
	for _, name := range namesOf(t, imageTwo) {
		fixture.store.sourceLabeled[name] = true
	}
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	if len(k0sDirEntries(t, fixture.deps.Root)) != 0 {
		t.Fatal("a leftover link of a held image must be removed")
	}
	for _, name := range namesOf(t, imageTwo) {
		if !fixture.store.pinned[name] || fixture.store.sourceLabeled[name] {
			t.Fatalf("%s must be pinned without the k0s label", name)
		}
	}
}

func TestSyncRemovesAnOrphanedLinkWhoseArchiveIsGone(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	writeFixtureFile(t, k0sFile(fixture.deps.Root, "v0.1.0-gone.tar"), "gone")
	if err := recordLinks(fixture.deps.Root, []string{"v0.1.0-gone.tar"}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	if exists(k0sFile(fixture.deps.Root, "v0.1.0-gone.tar")) || len(readLinkRecord(fixture.deps.Root)) != 0 {
		t.Fatal("a link without an archive twin must go")
	}
}

func TestSyncRemovesAnOrphanedLinkEvenWithoutAnyArchive(t *testing.T) {
	deps := newDeps(nil, time.Now())
	deps.Root = t.TempDir()
	writeFixtureFile(t, k0sFile(deps.Root, "v0.1.0-gone.tar"), "gone")
	if err := recordLinks(deps.Root, []string{"v0.1.0-gone.tar"}); err != nil {
		t.Fatal(err)
	}
	if err := syncImages(context.Background(), deps, &fakeStore{}); err != nil {
		t.Fatal(err)
	}
	if exists(k0sFile(deps.Root, "v0.1.0-gone.tar")) {
		t.Fatal("an orphaned link must go")
	}
}

func TestSyncKeepsAForeignFileInTheK0sDirectory(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	link := "v0.3.0-" + release.ImageFileName(imageTwo)
	writeFixtureFile(t, k0sFile(fixture.deps.Root, link), "someone else")
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	if readFixtureFile(t, k0sFile(fixture.deps.Root, link)) != "someone else" {
		t.Fatal("only a recorded link may be removed")
	}
}

func TestSyncUnlinksAnImportThatNeverFinishesAndBacksOff(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.store.neverLoad = true
	if err := fixture.sync(t); err != nil {
		t.Fatalf("linking is not an error: %v", err)
	}
	if len(k0sDirEntries(t, fixture.deps.Root)) == 0 {
		t.Fatal("missing tarballs must be linked")
	}
	*fixture.clock = fixture.clock.Add(30 * time.Second)
	if err := fixture.sync(t); err != nil || len(k0sDirEntries(t, fixture.deps.Root)) == 0 {
		t.Fatalf("the import may still run: %v", err)
	}
	*fixture.clock = fixture.clock.Add(time.Minute)
	err := fixture.sync(t)
	if err == nil || !strings.Contains(err.Error(), "not imported") {
		t.Fatalf("error %v must say the import did not finish", err)
	}
	if len(k0sDirEntries(t, fixture.deps.Root)) != 0 || len(readLinkRecord(fixture.deps.Root)) != 0 {
		t.Fatal("a failed import must not leave links for every k0s start")
	}
	if cond := importsCondition(fixture.deps, 1); len(cond) != 1 || cond[0].Status != "False" || cond[0].Reason != "ImportFailed" {
		t.Fatalf("the host must report the failure: %v", cond)
	}
	if err := fixture.sync(t); err != nil || len(k0sDirEntries(t, fixture.deps.Root)) != 0 {
		t.Fatalf("no new attempt during the backoff: %v", err)
	}
	*fixture.clock = fixture.clock.Add(2 * time.Minute)
	if err := fixture.sync(t); err != nil || len(k0sDirEntries(t, fixture.deps.Root)) == 0 {
		t.Fatalf("a new attempt after the backoff: %v", err)
	}
}

func TestSyncReportsNoFailureConditionWhenEverythingIsImported(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	if cond := importsCondition(fixture.deps, 1); len(cond) != 0 {
		t.Fatalf("condition %v", cond)
	}
}

func TestSyncReturnsAPinFailure(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.holdAll()
	fixture.store.pinned[pauseRef] = false
	fixture.store.pinErr = errors.New("containerd is down")
	if err := fixture.sync(t); err == nil || !strings.Contains(err.Error(), "containerd is down") {
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

func TestSyncPinsTheAirgapImagesFromTheTarballIndex(t *testing.T) {
	fixture := newSyncFixture(t)
	if err := os.Remove(archiveFile(fixture.deps.Root, "v0.3.0", release.BundleFileName)); err != nil {
		t.Fatal(err)
	}
	fixture.store.held[pauseRef] = true
	fixture.store.held["quay.io/k0sproject/pause@sha256:aa"] = true
	fixture.store.neverLoad = true
	if err := fixture.sync(t); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fixture.store.pinCalls, pauseRef) {
		t.Fatalf("names read from the airgap index must be pinned: %v", fixture.store.pinCalls)
	}
	if entries := k0sDirEntries(t, fixture.deps.Root); len(entries) != 0 {
		t.Fatalf("tarballs with unknown names must not be linked: %v", entries)
	}
}

func TestTarballImageNamesReadsTheK0sAirgapLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.tar")
	writeFixtureFile(t, path, string(k0sAirgapIndex(t)))
	names, err := tarballImageNames(path)
	want := []string{pauseRef, "quay.io/k0sproject/pause@sha256:aa"}
	if err != nil || !slices.Equal(names, want) {
		t.Fatalf("names %v err %v, want %v", names, err, want)
	}
}

func TestTarballImageNamesPrefersTheContainerdAnnotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.tar")
	writeFixtureFile(t, path, string(tarWith(t, "index.json", `{"manifests":[{"annotations":{"io.containerd.image.name":"docker.io/a/b:1","org.opencontainers.image.ref.name":"1"}}]}`)))
	names, err := tarballImageNames(path)
	if err != nil || !slices.Equal(names, []string{"docker.io/a/b:1"}) {
		t.Fatalf("names %v err %v", names, err)
	}
}

func TestTarballImageNamesUsesATagOnlyRefNameAsK0sImportsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.tar")
	writeFixtureFile(t, path, string(tarWith(t, "index.json", `{"manifests":[{"annotations":{"org.opencontainers.image.ref.name":"3.10"}}]}`)))
	names, err := tarballImageNames(path)
	if err != nil || !slices.Equal(names, []string{"3.10"}) {
		t.Fatalf("names %v err %v", names, err)
	}
}

func TestTarballImageNamesNormalizesDockerManifestTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.tar")
	writeFixtureFile(t, path, string(tarWith(t, "manifest.json", `[{"RepoTags":["nginx:latest","registry.k8s.io/pause:3.10"]}]`)))
	names, err := tarballImageNames(path)
	want := []string{"docker.io/library/nginx:latest", "registry.k8s.io/pause:3.10"}
	if err != nil || !slices.Equal(names, want) {
		t.Fatalf("names %v err %v, want %v", names, err, want)
	}
}

func TestCtrStoreListParsesLabelsByKey(t *testing.T) {
	exec := &host.FakeExec{Responses: map[string]string{
		"/usr/local/bin/k0s ctr --namespace k8s.io images ls": "REF TYPE DIGEST SIZE PLATFORMS LABELS\n" +
			"a/b:1 application/vnd.oci.image.index.v1+json sha256:aa 1.2 MiB linux/amd64 io.cri-containerd.pinned=pinned,io.k0sproject.ocibundle-paths=[{\"p\":\"/x\",\"t\":1}]\n" +
			"c/d:2 application/vnd.oci.image.index.v1+json sha256:bb 1.2 MiB linux/amd64 -\n",
	}}
	images, err := ctrStore{exec: exec, timeout: time.Second}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []storedImage{{Name: "a/b:1", Pinned: true, SourceLabeled: true}, {Name: "c/d:2"}}
	if !slices.Equal(images, want) {
		t.Fatalf("images %v, want %v", images, want)
	}
}

func TestCtrStorePinSetsOurLabelAndClearsTheK0sLabel(t *testing.T) {
	exec := &host.FakeExec{ResponsePrefixes: map[string]string{"/usr/local/bin/k0s ctr": ""}}
	if err := (ctrStore{exec: exec, timeout: time.Second}).Pin(context.Background(), "a/b:1"); err != nil {
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

func TestCtrStoreUnpinClearsOurLabel(t *testing.T) {
	exec := &host.FakeExec{ResponsePrefixes: map[string]string{"/usr/local/bin/k0s ctr": ""}}
	if err := (ctrStore{exec: exec, timeout: time.Second}).Unpin(context.Background(), "a/b:1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/k0s ctr --namespace k8s.io images label a/b:1 io.cri-containerd.pinned="}
	if !slices.Equal(exec.Calls, want) {
		t.Fatalf("calls %v, want %v", exec.Calls, want)
	}
}

type hangingExec struct{}

func (hangingExec) Run(ctx context.Context, _ string, _ ...string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestCtrStoreBoundsEveryCall(t *testing.T) {
	store := ctrStore{exec: hangingExec{}, timeout: 20 * time.Millisecond}
	started := time.Now()
	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("a hung containerd must end the call with an error")
	}
	if err := store.Pin(context.Background(), "a/b:1"); err == nil {
		t.Fatal("a hung containerd must end the call with an error")
	}
	if err := store.Unpin(context.Background(), "a/b:1"); err == nil {
		t.Fatal("a hung containerd must end the call with an error")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("calls must not wait for a hung containerd")
	}
}

func TestUnpinStaleUnpinsOnlyImagesOfDroppedTarballs(t *testing.T) {
	store := &fakeStore{held: map[string]bool{"gone:1": true, "kept:1": true, "foreign:1": true}, pinned: map[string]bool{"gone:1": true, "kept:1": true, "foreign:1": true}}
	before := []archivedTarball{{Names: []string{"gone:1"}}, {Names: []string{"kept:1"}}}
	after := []archivedTarball{{Names: []string{"kept:1"}}}
	if err := unpinStale(context.Background(), store, before, after); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(store.unpinCalls, []string{"gone:1"}) {
		t.Fatalf("unpinned %v", store.unpinCalls)
	}
}
