package agent

import (
	"archive/tar"
	"cmp"
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

	"filippo.io/age"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	keptBackups         = 3
	stampLayout         = "20060102T150405Z"
	upgradeBackupPrefix = "bedrock-"
	archiveSuffix       = ".tar.gz"
	sealedSuffix        = archiveSuffix + seal.Suffix
	partialSuffix       = ".partial"
)

var ovnDatabases = []string{"ovnnb_db.db", "ovnsb_db.db"}

func backup(ctx context.Context, env StepEnv) (Outcome, error) {
	name := fmt.Sprintf("bedrock-%s-%s%s", env.Upgrade.Spec.From, env.Deps.Now().UTC().Format(stampLayout), sealedSuffix)
	recipients, err := sealRecipients(ctx, env.Client, env.Deps, name)
	if err != nil {
		return Outcome{}, err
	}
	path, sum, err := writeBackup(ctx, env.Deps, name, upgradeBackupPrefix, keptBackups, recipients)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: path + " " + sum}, nil
}

func sealRecipients(ctx context.Context, c client.Client, deps Deps, name string) ([]age.Recipient, error) {
	identity, err := seal.EnsureHostIdentity(deps.Root)
	if err != nil {
		return nil, fmt.Errorf("host backup key: %w", err)
	}
	return archiveRecipients(ctx, c, identity.Recipient(), name, logLine), nil
}

func logLine(line string) {
	fmt.Fprintf(os.Stderr, "agent: %s\n", line)
}

func writeBackup(ctx context.Context, deps Deps, name, prefix string, keep int, recipients []age.Recipient) (string, string, error) {
	dir := filepath.Join(deps.Root, backupDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	if err := removePartials(dir, prefix); err != nil {
		return "", "", err
	}
	work, err := os.MkdirTemp(dir, ".work-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(work)
	if _, err := deps.Exec.Run(ctx, k0s.DefaultBinary, "backup", "--save-path", work); err != nil {
		return "", "", err
	}
	if err := copyOVN(filepath.Join(deps.Root, ovnDir), filepath.Join(work, "ovn")); err != nil {
		return "", "", err
	}
	archive := filepath.Join(dir, name)
	partial := archive + partialSuffix
	if err := writeArchive(work, partial, recipients); err != nil {
		return "", "", errors.Join(err, os.Remove(partial))
	}
	if err := os.Rename(partial, archive); err != nil {
		return "", "", errors.Join(err, os.Remove(partial))
	}
	sum, err := release.FileSHA256(archive)
	if err != nil {
		return "", "", err
	}
	if err := keepNewest(dir, prefix, keep); err != nil {
		return "", "", err
	}
	return filepath.Join("/", backupDir, name), sum, nil
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

func writeArchive(src, dest string, recipients []age.Recipient) error {
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	return errors.Join(sealTarGz(file, src, recipients), file.Sync(), file.Close())
}

func sealTarGz(dst io.Writer, src string, recipients []age.Recipient) error {
	reader, writer := io.Pipe()
	packed := make(chan error, 1)
	go func() {
		err := writeTarGz(writer, src)
		writer.CloseWithError(err)
		packed <- err
	}()
	sealErr := seal.Seal(dst, reader, recipients)
	reader.CloseWithError(sealErr)
	return cmp.Or(<-packed, sealErr)
}

func writeTarGz(dst io.Writer, src string) error {
	gz := gzip.NewWriter(dst)
	writer := tar.NewWriter(gz)
	walkErr := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		return addToArchive(writer, src, path)
	})
	return errors.Join(walkErr, writer.Close(), gz.Close())
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

func keepNewest(dir, prefix string, keep int) error {
	archives, err := archivePaths(dir, prefix, "")
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

func archivePaths(dir, prefix, tail string) ([]string, error) {
	var paths []string
	for _, suffix := range []string{archiveSuffix, sealedSuffix} {
		matches, err := filepath.Glob(filepath.Join(dir, prefix+"*"+suffix+tail))
		if err != nil {
			return nil, err
		}
		paths = append(paths, matches...)
	}
	return paths, nil
}

func backupStamp(path string) string {
	name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), seal.Suffix), archiveSuffix)
	return name[strings.LastIndex(name, "-")+1:]
}

func removePartials(dir, prefix string) error {
	partials, err := archivePaths(dir, prefix, partialSuffix)
	if err != nil {
		return err
	}
	for _, partial := range partials {
		if err := os.Remove(partial); err != nil {
			return err
		}
	}
	return nil
}
