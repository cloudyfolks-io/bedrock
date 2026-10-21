package agent

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	pinnedLabelKey    = "io.cri-containerd.pinned"
	pinnedLabel       = pinnedLabelKey + "=pinned"
	k0sSourceLabelKey = "io.k0sproject.ocibundle-paths"
	ctrCallTimeout    = 30 * time.Second
	importBackoffBase = time.Minute
	importBackoffMax  = time.Hour
	containerdNames   = "io.containerd.image.name"
	ociRefName        = "org.opencontainers.image.ref.name"
)

type storedImage struct {
	Name          string
	Pinned        bool
	SourceLabeled bool
}

type imageStore interface {
	List(ctx context.Context) ([]storedImage, error)
	Pin(ctx context.Context, name string) error
	Unpin(ctx context.Context, name string) error
}

type ctrStore struct {
	exec    host.Exec
	timeout time.Duration
}

func (s ctrStore) run(ctx context.Context, args ...string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.exec.Run(callCtx, k0s.DefaultBinary, append([]string{"ctr", "--namespace", "k8s.io", "images"}, args...)...)
}

func (s ctrStore) List(ctx context.Context) ([]storedImage, error) {
	out, err := s.run(ctx, "ls")
	if err != nil {
		return nil, err
	}
	var images []storedImage
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "REF" {
			continue
		}
		labels := strings.Join(fields[5:], " ")
		images = append(images, storedImage{
			Name:          fields[0],
			Pinned:        strings.Contains(labels, pinnedLabel),
			SourceLabeled: strings.Contains(labels, k0sSourceLabelKey+"="),
		})
	}
	return images, nil
}

func (s ctrStore) Pin(ctx context.Context, name string) error {
	if _, err := s.run(ctx, "label", name, k0sSourceLabelKey+"="); err != nil {
		return err
	}
	_, err := s.run(ctx, "label", name, pinnedLabel)
	return err
}

func (s ctrStore) Unpin(ctx context.Context, name string) error {
	_, err := s.run(ctx, "label", name, pinnedLabelKey+"=")
	return err
}

type archivedTarball struct {
	Version string
	File    string
	Path    string
	Names   []string
}

func (t archivedTarball) linkName() string {
	return t.Version + "-" + t.File
}

func (t archivedTarball) importedIn(images []storedImage) bool {
	return len(t.Names) > 0 && allMatch(t.Names, images, func(storedImage) bool { return true })
}

func allMatch(names []string, images []storedImage, ok func(storedImage) bool) bool {
	for _, name := range names {
		if !slices.ContainsFunc(images, func(image storedImage) bool { return image.Name == name && ok(image) }) {
			return false
		}
	}
	return true
}

func linkArchiveWhenStoreIsEmpty(root, version string) error {
	if _, err := os.Stat(filepath.Join(root, containerdStore)); err == nil {
		return nil
	}
	tarballs, err := archivedTarballs(root)
	if err != nil {
		return err
	}
	return linkTarballs(root, slices.DeleteFunc(tarballs, func(t archivedTarball) bool { return t.Version != version }))
}

func syncHostImages(ctx context.Context, deps Deps) error {
	if !runsContainers(deps.Root) {
		return nil
	}
	return syncImages(ctx, deps, ctrStore{exec: deps.Exec, timeout: ctrCallTimeout})
}

func syncImages(ctx context.Context, deps Deps, store imageStore) error {
	tarballs, err := archivedTarballs(deps.Root)
	if err != nil {
		return err
	}
	if err := removeOrphanLinks(deps.Root, tarballs); err != nil {
		return err
	}
	if len(tarballs) == 0 {
		return nil
	}
	warnUnnamed(tarballs)
	images, err := store.List(ctx)
	if err != nil {
		return err
	}
	if err := settle(ctx, deps.Root, store, tarballs, images); err != nil {
		return err
	}
	return advanceImport(deps, tarballs, images)
}

func warnUnnamed(tarballs []archivedTarball) {
	for _, tarball := range tarballs {
		if len(tarball.Names) == 0 {
			logLine(fmt.Sprintf("image tarball %s has no readable image name, it is not imported or pinned", tarball.Path))
		}
	}
}

func settle(ctx context.Context, root string, store imageStore, tarballs []archivedTarball, images []storedImage) error {
	var imported []archivedTarball
	for _, tarball := range tarballs {
		if tarball.importedIn(images) {
			imported = append(imported, tarball)
		}
	}
	if err := pinUnsettled(ctx, store, imported, images); err != nil {
		return err
	}
	return unlinkTarballs(root, imported)
}

func pinUnsettled(ctx context.Context, store imageStore, tarballs []archivedTarball, images []storedImage) error {
	done := map[string]bool{}
	for _, image := range images {
		done[image.Name] = image.Pinned && !image.SourceLabeled
	}
	for _, tarball := range tarballs {
		for _, name := range tarball.Names {
			if done[name] {
				continue
			}
			if err := store.Pin(ctx, name); err != nil {
				return fmt.Errorf("pin %s: %w", name, err)
			}
			done[name] = true
		}
	}
	return nil
}

func unpinStale(ctx context.Context, store imageStore, before, after []archivedTarball) error {
	stale := namesOfTarballs(before)
	kept := namesOfTarballs(after)
	images, err := store.List(ctx)
	if err != nil {
		return err
	}
	for _, image := range images {
		if image.Pinned && slices.Contains(stale, image.Name) && !slices.Contains(kept, image.Name) {
			if err := store.Unpin(ctx, image.Name); err != nil {
				return fmt.Errorf("unpin %s: %w", image.Name, err)
			}
		}
	}
	return nil
}

func namesOfTarballs(tarballs []archivedTarball) []string {
	var names []string
	for _, tarball := range tarballs {
		names = append(names, tarball.Names...)
	}
	return names
}

type importState struct {
	LinkedAt  *time.Time `json:"linkedAt,omitempty"`
	Attempts  int        `json:"attempts,omitempty"`
	NextAt    *time.Time `json:"nextAt,omitempty"`
	LastError string     `json:"lastError,omitempty"`
}

func advanceImport(deps Deps, tarballs []archivedTarball, images []storedImage) error {
	var missing []archivedTarball
	for _, tarball := range tarballs {
		if len(tarball.Names) > 0 && !tarball.importedIn(images) {
			missing = append(missing, tarball)
		}
	}
	state := readImportState(deps.Root)
	now := deps.Now()
	switch {
	case len(missing) == 0:
		return writeImportState(deps.Root, importState{})
	case state.LinkedAt != nil && now.Sub(*state.LinkedAt) > deps.K0sTimeout:
		return failImport(deps, state, missing, now)
	case state.LinkedAt != nil, state.NextAt != nil && now.Before(*state.NextAt):
		return nil
	}
	if err := linkTarballs(deps.Root, missing); err != nil {
		return err
	}
	state.LinkedAt = &now
	return writeImportState(deps.Root, state)
}

func failImport(deps Deps, state importState, missing []archivedTarball, now time.Time) error {
	if err := unlinkTarballs(deps.Root, missing); err != nil {
		return err
	}
	attempts := state.Attempts + 1
	next := now.Add(min(importBackoffBase<<(attempts-1), importBackoffMax))
	message := fmt.Sprintf("k0s did not import the linked images within %s: image %s is not imported", deps.K0sTimeout, missing[0].Names[0])
	if err := writeImportState(deps.Root, importState{Attempts: attempts, NextAt: &next, LastError: message}); err != nil {
		return err
	}
	return errors.New(message)
}

func importsCondition(deps Deps, generation int64) []metav1.Condition {
	state := readImportState(deps.Root)
	if state.LastError == "" {
		return nil
	}
	return []metav1.Condition{condition(v1alpha1.ConditionImagesImported, metav1.ConditionFalse, "ImportFailed", state.LastError, generation)}
}

func readImportState(root string) importState {
	raw, err := os.ReadFile(filepath.Join(root, importStateFile))
	if err != nil {
		return importState{}
	}
	var state importState
	if json.Unmarshal(raw, &state) != nil {
		return importState{}
	}
	return state
}

func writeImportState(root string, state importState) error {
	file := filepath.Join(root, importStateFile)
	if state == (importState{}) {
		return removeIfPresent(file)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, raw, 0o644)
}

func removeIfPresent(file string) error {
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func readLinkRecord(root string) []string {
	raw, err := os.ReadFile(filepath.Join(root, linkRecordFile))
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

func writeLinkRecord(root string, names []string) error {
	file := filepath.Join(root, linkRecordFile)
	if len(names) == 0 {
		return removeIfPresent(file)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(strings.Join(names, "\n")+"\n"), 0o644)
}

func recordLinks(root string, names []string) error {
	recorded := readLinkRecord(root)
	for _, name := range names {
		recorded = appendMissing(recorded, name)
	}
	return writeLinkRecord(root, recorded)
}

func linkTarballs(root string, tarballs []archivedTarball) error {
	names := make([]string, 0, len(tarballs))
	for _, tarball := range tarballs {
		names = append(names, tarball.linkName())
	}
	if err := recordLinks(root, names); err != nil {
		return err
	}
	for _, tarball := range tarballs {
		if err := os.MkdirAll(filepath.Join(root, k0sImagesDir), 0o755); err != nil {
			return err
		}
		err := os.Link(tarball.Path, filepath.Join(root, k0sImagesDir, tarball.linkName()))
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}

func unlinkTarballs(root string, tarballs []archivedTarball) error {
	names := make([]string, 0, len(tarballs))
	for _, tarball := range tarballs {
		names = append(names, tarball.linkName())
	}
	return unlinkRecorded(root, names)
}

func removeOrphanLinks(root string, tarballs []archivedTarball) error {
	var current []string
	for _, tarball := range tarballs {
		current = append(current, tarball.linkName())
	}
	var orphans []string
	for _, name := range readLinkRecord(root) {
		if !slices.Contains(current, name) {
			orphans = append(orphans, name)
		}
	}
	return unlinkRecorded(root, orphans)
}

func unlinkRecorded(root string, names []string) error {
	recorded := readLinkRecord(root)
	for _, name := range names {
		if !slices.Contains(recorded, name) {
			continue
		}
		if err := removeIfPresent(filepath.Join(root, k0sImagesDir, name)); err != nil {
			return err
		}
	}
	return writeLinkRecord(root, slices.DeleteFunc(recorded, func(name string) bool { return slices.Contains(names, name) }))
}

func archivedTarballs(root string) ([]archivedTarball, error) {
	versions, err := os.ReadDir(filepath.Join(root, archiveDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tarballs []archivedTarball
	for _, version := range versions {
		found, err := versionTarballs(archiveVersionDir(root, version.Name()), version.Name())
		if err != nil {
			return nil, err
		}
		tarballs = append(tarballs, found...)
	}
	return tarballs, nil
}

func versionTarballs(dir, version string) ([]archivedTarball, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	known := namesByFile(dir)
	var tarballs []archivedTarball
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".tar") {
			continue
		}
		file := filepath.Join(dir, entry.Name())
		names, listed := known[entry.Name()]
		if !listed {
			names, _ = tarballImageNames(file)
		}
		tarballs = append(tarballs, archivedTarball{Version: version, File: entry.Name(), Path: file, Names: names})
	}
	return tarballs, nil
}

func namesByFile(dir string) map[string][]string {
	spec, err := readBundleSpec(filepath.Join(dir, release.BundleFileName))
	if err != nil {
		return nil
	}
	byFile := map[string][]string{}
	for _, image := range spec.Images {
		if names, err := release.ContainerdNames(image.Ref); err == nil {
			byFile[image.File] = names
		}
	}
	return byFile
}

func tarballImageNames(file string) ([]string, error) {
	in, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	reader := tar.NewReader(in)
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		switch path.Clean(header.Name) {
		case "index.json":
			names = append(names, indexNames(reader)...)
		case "manifest.json":
			names = append(names, manifestNames(reader)...)
		}
	}
}

func indexNames(in io.Reader) []string {
	var index struct {
		Manifests []struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if json.NewDecoder(in).Decode(&index) != nil {
		return nil
	}
	var names []string
	for _, manifest := range index.Manifests {
		if name := annotatedName(manifest.Annotations); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func annotatedName(annotations map[string]string) string {
	if name := annotations[containerdNames]; name != "" {
		return name
	}
	return annotations[ociRefName]
}

func manifestNames(in io.Reader) []string {
	var manifests []struct {
		RepoTags []string `json:"RepoTags"`
	}
	if json.NewDecoder(in).Decode(&manifests) != nil {
		return nil
	}
	var names []string
	for _, manifest := range manifests {
		for _, tag := range manifest.RepoTags {
			if normalized, err := release.ContainerdNames(tag); err == nil {
				names = append(names, normalized...)
			}
		}
	}
	return names
}
