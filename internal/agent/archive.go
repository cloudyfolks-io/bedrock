package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

var versionedAirgapName = regexp.MustCompile(`^k0s-airgap-(.+)\.tar$`)

func archiveVersionDir(root, version string) string {
	return filepath.Join(root, archiveDir, version, runtime.GOARCH)
}

func adoptK0sImages(root, version string) error {
	entries, err := os.ReadDir(filepath.Join(root, k0sImagesDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		targetVersion, name := archiveTarget(entry.Name(), version)
		if err := moveIntoArchive(filepath.Join(root, k0sImagesDir, entry.Name()), filepath.Join(archiveVersionDir(root, targetVersion), name)); err != nil {
			return err
		}
	}
	return nil
}

func archiveTarget(file, version string) (string, string) {
	if match := versionedAirgapName.FindStringSubmatch(file); match != nil {
		return match[1], airgapFile
	}
	return version, file
}

func moveIntoArchive(src, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return os.Remove(src)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	err := os.Rename(src, dest)
	if errors.Is(err, syscall.EXDEV) {
		if err := release.CopyFile(src, dest); err != nil {
			return err
		}
		return os.Remove(src)
	}
	return err
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
	return nil
}

func carryFiles(fromDir, toDir string, names []string) error {
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(fromDir, name)); err != nil {
			continue
		}
		if err := keepTarball(filepath.Join(fromDir, name), filepath.Join(toDir, name)); err != nil {
			return err
		}
	}
	return nil
}

func archiveFileNames(refs []string) []string {
	names := []string{airgapFile}
	for _, ref := range refs {
		names = append(names, release.ImageFileName(ref))
	}
	return names
}
