package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/depot"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

var versionedAirgapName = regexp.MustCompile(`^k0s-airgap-(.+)\.tar$`)

func archiveVersionDir(root, version string) string {
	return filepath.Join(root, archiveDir, version, runtime.GOARCH)
}

func releaseLists(ctx context.Context, c client.Reader, root string) map[string][]string {
	lists := map[string][]string{}
	for _, base := range []string{filepath.Join(root, archiveDir), filepath.Join(root, depot.Dir)} {
		versions, _ := os.ReadDir(base)
		for _, version := range versions {
			spec, err := readBundleSpec(filepath.Join(base, version.Name(), runtime.GOARCH, release.BundleFileName))
			if err != nil {
				continue
			}
			for _, image := range spec.Images {
				lists[version.Name()] = appendMissing(lists[version.Name()], image.File)
			}
		}
	}
	if c == nil {
		return lists
	}
	var releases v1alpha1.ReleaseList
	if err := c.List(ctx, &releases); err != nil {
		return lists
	}
	for _, item := range releases.Items {
		for _, ref := range releaseRefs(item) {
			lists[item.Spec.Version] = appendMissing(lists[item.Spec.Version], release.ImageFileName(ref))
		}
	}
	return lists
}

func releaseRefs(r v1alpha1.Release) []string {
	refs := slices.Clone(r.Spec.Images)
	if r.Spec.Image != "" {
		refs = appendMissing(refs, r.Spec.Image)
	}
	return refs
}

func appendMissing(list []string, item string) []string {
	if slices.Contains(list, item) {
		return list
	}
	return append(list, item)
}

func adoptK0sImages(root string, lists map[string][]string, fallbackVersion string) error {
	entries, err := os.ReadDir(filepath.Join(root, k0sImagesDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	recorded := readLinkRecord(root)
	archived := archivedFileInfos(root)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || slices.Contains(recorded, entry.Name()) {
			continue
		}
		source := filepath.Join(root, k0sImagesDir, entry.Name())
		if isArchivedFile(source, archived) {
			continue
		}
		targets := filingTargets(entry.Name(), lists, fallbackVersion)
		if len(targets) == 0 {
			logLine(fmt.Sprintf("image tarball %s is not in any release image list, k0s keeps importing it", entry.Name()))
			continue
		}
		if err := fileInto(source, targets, root); err != nil {
			return err
		}
	}
	return nil
}

type filingTarget struct{ version, name string }

func filingTargets(file string, lists map[string][]string, fallbackVersion string) []filingTarget {
	if match := versionedAirgapName.FindStringSubmatch(file); match != nil {
		return []filingTarget{{match[1], airgapFile}}
	}
	if file == airgapFile {
		return []filingTarget{{fallbackVersion, airgapFile}}
	}
	var targets []filingTarget
	for version, files := range lists {
		if slices.Contains(files, file) {
			targets = append(targets, filingTarget{version, file})
		}
	}
	return targets
}

func fileInto(source string, targets []filingTarget, root string) error {
	complete := true
	for _, target := range targets {
		placed, err := placeCopy(source, filepath.Join(archiveVersionDir(root, target.version), target.name))
		if err != nil {
			return err
		}
		complete = complete && placed
	}
	if !complete {
		logLine(fmt.Sprintf("%s differs from an archived file of the same name, kept in the k0s directory", filepath.Base(source)))
		return nil
	}
	return os.Remove(source)
}

func placeCopy(source, dest string) (bool, error) {
	if _, err := os.Stat(dest); err == nil {
		return sameContent(source, dest)
	}
	if err := keepTarball(source, dest); err != nil {
		return false, err
	}
	return sameContent(source, dest)
}

func sameContent(a, b string) (bool, error) {
	infoA, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	infoB, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if os.SameFile(infoA, infoB) {
		return true, nil
	}
	if infoA.Size() != infoB.Size() {
		return false, nil
	}
	sumA, err := fileDigest(a)
	if err != nil {
		return false, err
	}
	sumB, err := fileDigest(b)
	return err == nil && sumA == sumB, err
}

func fileDigest(path string) (string, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, in); err != nil {
		return "", err
	}
	return string(hash.Sum(nil)), nil
}

func archivedFileInfos(root string) []os.FileInfo {
	var infos []os.FileInfo
	filepath.WalkDir(filepath.Join(root, archiveDir), func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			infos = append(infos, info)
		}
		return nil
	})
	return infos
}

func isArchivedFile(path string, archived []os.FileInfo) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(archived, func(other os.FileInfo) bool { return os.SameFile(info, other) })
}

func retainArchive(root, keepVersion string, keepFiles []string) error {
	base := filepath.Join(root, archiveDir)
	keepDir := archiveVersionDir(root, keepVersion)
	if _, err := os.Stat(keepDir); err != nil {
		return nil
	}
	versions, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if version.Name() == keepVersion {
			continue
		}
		if err := carryFiles(archiveVersionDir(root, version.Name()), keepDir, keepFiles); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(base, version.Name())); err != nil {
			return err
		}
	}
	return removeUnlisted(keepDir, keepFiles)
}

func carryFiles(fromDir, toDir string, names []string) error {
	for _, name := range names {
		if name == airgapFile || name == release.BundleFileName {
			continue
		}
		if _, err := os.Stat(filepath.Join(fromDir, name)); err != nil {
			continue
		}
		if err := keepTarball(filepath.Join(fromDir, name), filepath.Join(toDir, name)); err != nil {
			return err
		}
	}
	return nil
}

func removeUnlisted(dir string, keep []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if slices.Contains(keep, entry.Name()) || strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func archiveFileNames(refs []string) []string {
	names := []string{airgapFile, release.BundleFileName}
	for _, ref := range refs {
		names = append(names, release.ImageFileName(ref))
	}
	return names
}
