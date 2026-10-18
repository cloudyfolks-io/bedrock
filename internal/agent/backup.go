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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"filippo.io/age"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	keptBackups         = 3
	stampLayout         = "20060102T150405Z"
	upgradeBackupPrefix = "bedrock-"
	archiveSuffix       = ".tar.gz"
	sealedSuffix        = archiveSuffix + seal.Suffix
	partialSuffix       = ".partial"
	newHostKeyLine      = "host backup key " + seal.HostKeyFile + " is new; older sealed archives open only with an admin identity"
)

var ovnDatabases = []string{"ovnnb_db.db", "ovnsb_db.db"}

func backup(ctx context.Context, env StepEnv) (Outcome, error) {
	name := fmt.Sprintf("bedrock-%s-%s%s", env.Upgrade.Spec.From, env.Deps.Now().UTC().Format(stampLayout), sealedSuffix)
	recipients, err := sealRecipients(ctx, env.Client, env.Deps, name)
	if err != nil {
		return Outcome{}, err
	}
	path, sum, err := writeBackup(ctx, env.Deps, name, upgradeBackupPrefix, keptBackups, recipients, plainK0sBackup)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: path + " " + sum}, nil
}

func sealRecipients(ctx context.Context, c client.Client, deps Deps, name string) ([]age.Recipient, error) {
	identity, err := ensureHostKey(deps.Root, logLine)
	if err != nil {
		return nil, fmt.Errorf("host backup key: %w", err)
	}
	return archiveRecipients(ctx, c, identity.Recipient(), name, logLine)
}

func ensureHostKey(root string, log func(string)) (*age.X25519Identity, error) {
	_, statErr := os.Stat(filepath.Join(root, seal.HostKeyFile))
	identity, err := seal.EnsureHostIdentity(root)
	if err != nil {
		return nil, err
	}
	if errors.Is(statErr, fs.ErrNotExist) && sealedArchivesExist(root) {
		log(newHostKeyLine)
	}
	return identity, nil
}

func sealedArchivesExist(root string) bool {
	matches, err := filepath.Glob(filepath.Join(root, backupDir, "*"+sealedSuffix))
	return err == nil && len(matches) > 0
}

func logLine(line string) {
	fmt.Fprintf(os.Stderr, "agent: %s\n", line)
}

func writeBackup(ctx context.Context, deps Deps, name, prefix string, keep int, recipients []age.Recipient, k0sBackup k0sBackupStep) (string, string, error) {
	dir := filepath.Join(deps.Root, backupDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	if err := removePartials(dir, prefix); err != nil {
		return "", "", err
	}
	work, err := os.MkdirTemp(dir, workDirPrefix)
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(work)
	if err := k0sBackup(ctx, deps, work); err != nil {
		return "", "", err
	}
	if err := copyOVN(filepath.Join(deps.Root, ovnDir), filepath.Join(work, "ovn")); err != nil {
		return "", "", err
	}
	archive := filepath.Join(dir, name)
	if err := writeArchive(work, archive, recipients); err != nil {
		return "", "", err
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
	return writeAtomic(dest, func(dst io.Writer) error { return sealTarGz(dst, src, recipients) })
}

func writeSynced(dest string, fill func(io.Writer) error) error {
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	return errors.Join(fill(file), file.Sync(), file.Close())
}

func writeAtomic(dest string, fill func(io.Writer) error) error {
	partial := dest + partialSuffix
	if err := writeSynced(partial, fill); err != nil {
		return errors.Join(err, removeIfExists(partial))
	}
	if err := os.Rename(partial, dest); err != nil {
		return errors.Join(err, removeIfExists(partial))
	}
	return syncDir(filepath.Dir(dest))
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(handle.Sync(), handle.Close())
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
	gz, err := gzip.NewWriterLevel(dst, gzip.BestSpeed)
	if err != nil {
		return err
	}
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
	files := make(map[string][]string)
	for _, path := range archives {
		base := archiveBase(path)
		files[base] = append(files[base], path)
	}
	bases := slices.SortedFunc(maps.Keys(files), compareBases)
	for _, old := range bases[:max(0, len(bases)-keep)] {
		if err := removeFiles(files[old]); err != nil {
			return err
		}
	}
	return nil
}

func removeFiles(paths []string) error {
	for _, path := range paths {
		if err := removeIfExists(path); err != nil {
			return err
		}
	}
	return nil
}

func compareBases(a, b string) int {
	return cmp.Or(strings.Compare(stampOf(a), stampOf(b)), strings.Compare(a, b))
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

func archiveBase(path string) string {
	return strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), seal.Suffix), archiveSuffix)
}

func stampOf(base string) string {
	return base[strings.LastIndex(base, "-")+1:]
}

func backupStamp(path string) string {
	return stampOf(archiveBase(path))
}

func removePartials(dir, prefix string) error {
	partials, err := archivePaths(dir, prefix, partialSuffix)
	if err != nil {
		return err
	}
	return removeFiles(partials)
}
