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

	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	pinnedLabel       = "io.cri-containerd.pinned=pinned"
	k0sSourceLabelKey = "io.k0sproject.ocibundle-paths"
)

type storedImage struct {
	Name   string
	Pinned bool
}

type imageStore interface {
	List(ctx context.Context) ([]storedImage, error)
	Pin(ctx context.Context, name string) error
}

type ctrStore struct {
	exec host.Exec
}

func (s ctrStore) List(ctx context.Context) ([]storedImage, error) {
	out, err := s.exec.Run(ctx, k0s.DefaultBinary, "ctr", "--namespace", "k8s.io", "images", "ls")
	if err != nil {
		return nil, err
	}
	var images []storedImage
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "REF" {
			continue
		}
		images = append(images, storedImage{Name: fields[0], Pinned: slices.Contains(strings.Split(fields[len(fields)-1], ","), pinnedLabel)})
	}
	return images, nil
}

func (s ctrStore) Pin(ctx context.Context, name string) error {
	if _, err := s.exec.Run(ctx, k0s.DefaultBinary, "ctr", "--namespace", "k8s.io", "images", "label", name, k0sSourceLabelKey+"="); err != nil {
		return err
	}
	_, err := s.exec.Run(ctx, k0s.DefaultBinary, "ctr", "--namespace", "k8s.io", "images", "label", name, pinnedLabel)
	return err
}

type archivedTarball struct {
	Version string
	File    string
	Path    string
	Names   []string
}

func (t archivedTarball) linkName() string {
	if t.File == airgapFile {
		return versionedAirgap(t.Version)
	}
	return t.File
}

func syncHostImages(ctx context.Context, deps Deps) error {
	if !runsContainers(deps.Root) {
		return nil
	}
	return syncImages(ctx, deps, ctrStore{exec: deps.Exec})
}

func syncImages(ctx context.Context, deps Deps, store imageStore) error {
	tarballs, err := archivedTarballs(deps.Root)
	if err != nil || len(tarballs) == 0 {
		return err
	}
	images, err := store.List(ctx)
	if err != nil {
		return err
	}
	missing := tarballsMissingFrom(tarballs, heldNames(images))
	if err := linkTarballs(deps.Root, missing); err != nil {
		return err
	}
	if len(missing) > 0 {
		if err := waitForImport(ctx, deps, store, missing); err != nil {
			return err
		}
	}
	return settle(ctx, deps.Root, store, tarballs)
}

func settle(ctx context.Context, root string, store imageStore, tarballs []archivedTarball) error {
	images, err := store.List(ctx)
	if err != nil {
		return err
	}
	held := heldNames(images)
	var imported []archivedTarball
	for _, tarball := range tarballs {
		if len(tarball.Names) > 0 && containsAll(held, tarball.Names) {
			imported = append(imported, tarball)
		}
	}
	if err := unlinkTarballs(root, imported); err != nil {
		return err
	}
	return pinUnpinned(ctx, store, imported, images)
}

func pinUnpinned(ctx context.Context, store imageStore, tarballs []archivedTarball, images []storedImage) error {
	pinned := map[string]bool{}
	for _, image := range images {
		pinned[image.Name] = image.Pinned
	}
	for _, tarball := range tarballs {
		for _, name := range tarball.Names {
			if pinned[name] {
				continue
			}
			if err := store.Pin(ctx, name); err != nil {
				return fmt.Errorf("pin %s: %w", name, err)
			}
			pinned[name] = true
		}
	}
	return nil
}

func heldNames(images []storedImage) []string {
	names := make([]string, 0, len(images))
	for _, image := range images {
		names = append(names, image.Name)
	}
	return names
}

func tarballsMissingFrom(tarballs []archivedTarball, held []string) []archivedTarball {
	var missing []archivedTarball
	for _, tarball := range tarballs {
		if len(tarball.Names) > 0 && !containsAll(held, tarball.Names) {
			missing = append(missing, tarball)
		}
	}
	return missing
}

func waitForImport(ctx context.Context, deps Deps, store imageStore, wanted []archivedTarball) error {
	observed, err := waitClear(ctx, deps.K0sTimeout, deps.K0sPoll, func(probeCtx context.Context) string {
		images, err := store.List(probeCtx)
		if err != nil {
			return err.Error()
		}
		held := heldNames(images)
		for _, tarball := range wanted {
			for _, name := range tarball.Names {
				if !slices.Contains(held, name) {
					return "image " + name + " is not imported"
				}
			}
		}
		return ""
	})
	if err != nil {
		return fmt.Errorf("k0s did not import the linked images within %s: %s: %w", deps.K0sTimeout, observed, err)
	}
	return nil
}

func linkTarballs(root string, tarballs []archivedTarball) error {
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
	for _, tarball := range tarballs {
		link := filepath.Join(root, k0sImagesDir, tarball.linkName())
		linked, err := os.Stat(link)
		if err != nil {
			continue
		}
		archived, err := os.Stat(tarball.Path)
		if err != nil || !os.SameFile(linked, archived) {
			continue
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	return nil
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
		if name := manifest.Annotations["io.containerd.image.name"]; name != "" {
			names = append(names, name)
		}
	}
	return names
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
		names = append(names, manifest.RepoTags...)
	}
	return names
}
