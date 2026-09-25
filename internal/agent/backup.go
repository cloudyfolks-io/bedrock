package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cloudyfolks-labs/bedrock/internal/k0s"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

const keptBackups = 3

var ovnDatabases = []string{"ovnnb_db.db", "ovnsb_db.db"}

func backup(ctx context.Context, env StepEnv) (Outcome, error) {
	deps := env.Deps
	dir := filepath.Join(deps.Root, backupDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Outcome{}, err
	}
	work, err := os.MkdirTemp(dir, ".work-")
	if err != nil {
		return Outcome{}, err
	}
	defer os.RemoveAll(work)
	if _, err := deps.Exec.Run(ctx, k0s.DefaultBinary, "backup", "--save-path", work); err != nil {
		return Outcome{}, err
	}
	if err := copyOVN(filepath.Join(deps.Root, ovnDir), filepath.Join(work, "ovn")); err != nil {
		return Outcome{}, err
	}
	name := fmt.Sprintf("bedrock-%s-%s.tar.gz", env.Upgrade.Spec.From, deps.Now().UTC().Format("20060102T150405Z"))
	archive := filepath.Join(dir, name)
	if err := writeArchive(work, archive); err != nil {
		return Outcome{}, errors.Join(err, os.Remove(archive))
	}
	sum, err := release.FileSHA256(archive)
	if err != nil {
		return Outcome{}, err
	}
	if err := keepNewest(dir, keptBackups); err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: filepath.Join("/", backupDir, name) + " " + sum}, nil
}

func copyOVN(src, dest string) error {
	for _, name := range ovnDatabases {
		path := filepath.Join(src, name)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := os.MkdirAll(dest, 0o700); err != nil {
			return err
		}
		if err := release.CopyFile(path, filepath.Join(dest, name)); err != nil {
			return err
		}
	}
	return nil
}

func writeArchive(src, dest string) error {
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(file)
	writer := tar.NewWriter(gz)
	walkErr := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		return addToArchive(writer, src, path)
	})
	return errors.Join(walkErr, writer.Close(), gz.Close(), file.Close())
}

func addToArchive(writer *tar.Writer, base, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(rel)
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	_, err = io.Copy(writer, source)
	return err
}

func keepNewest(dir string, keep int) error {
	archives, err := filepath.Glob(filepath.Join(dir, "bedrock-*.tar.gz"))
	if err != nil {
		return err
	}
	slices.SortFunc(archives, func(a, b string) int { return strings.Compare(backupStamp(a), backupStamp(b)) })
	for _, old := range archives[:max(0, len(archives)-keep)] {
		if err := os.Remove(old); err != nil {
			return err
		}
	}
	return nil
}

func backupStamp(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".tar.gz")
	return name[strings.LastIndex(name, "-")+1:]
}
