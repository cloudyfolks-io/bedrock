package agent

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
)

const (
	workDirPrefix    = ".work-"
	restoreDirPrefix = ".restore-"
	staleWorkAge     = time.Hour
	plaintextLogName = "the plaintext archives"
)

func tidyBackupDir(ctx context.Context, c client.Client, deps Deps) error {
	return errors.Join(
		removeStaleLeftovers(deps.Root, deps.Now().Add(-staleWorkAge)),
		sealOlderArchives(ctx, c, deps),
		pruneRetired(deps.Root, deps.Now()),
	)
}

func removeStaleLeftovers(root string, cutoff time.Time) error {
	return errors.Join(removeStaleWorkDirs(root, cutoff), removeStalePlaintextPartials(root, cutoff))
}

func sealOlderArchives(ctx context.Context, c client.Client, deps Deps) error {
	pending, err := plaintextArchives(filepath.Join(deps.Root, backupDir), "")
	if err != nil || len(pending) == 0 {
		return err
	}
	recipients, err := sealRecipients(ctx, c, deps, plaintextLogName)
	if err != nil {
		return err
	}
	return sealPlaintextArchives(deps.Root, recipients)
}

func sealPlaintextArchives(root string, recipients []age.Recipient) error {
	plaintext, err := plaintextArchives(filepath.Join(root, backupDir), "")
	if err != nil {
		return err
	}
	results := make([]error, 0, len(plaintext))
	for _, path := range plaintext {
		results = append(results, sealPlaintext(path, recipients))
	}
	return errors.Join(results...)
}

func sealPlaintext(plaintext string, recipients []age.Recipient) error {
	sealed := plaintext + seal.Suffix
	if err := removeIfExists(sealed + partialSuffix); err != nil {
		return err
	}
	if err := ensureSealed(plaintext, sealed, recipients); err != nil {
		return err
	}
	return removeIfExists(plaintext)
}

func ensureSealed(plaintext, sealed string, recipients []age.Recipient) error {
	_, err := os.Stat(sealed)
	if errors.Is(err, fs.ErrNotExist) {
		return sealCopy(plaintext, sealed, recipients)
	}
	return err
}

func sealCopy(plaintext, sealed string, recipients []age.Recipient) error {
	src, err := os.Open(plaintext)
	if err != nil {
		return err
	}
	defer src.Close()
	return writeAtomic(sealed, func(dst io.Writer) error { return seal.Seal(dst, src, recipients) })
}

func plaintextArchives(dir, tail string) ([]string, error) {
	var paths []string
	for _, prefix := range []string{upgradeBackupPrefix, etcdSnapshotPrefix} {
		matches, err := filepath.Glob(filepath.Join(dir, prefix+"*"+archiveSuffix+tail))
		if err != nil {
			return nil, err
		}
		paths = append(paths, matches...)
	}
	return paths, nil
}

func removeStaleWorkDirs(root string, cutoff time.Time) error {
	dir := filepath.Join(root, backupDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	results := make([]error, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && isWorkDir(entry.Name()) {
			results = append(results, removeIfOlder(filepath.Join(dir, entry.Name()), cutoff))
		}
	}
	return errors.Join(results...)
}

func removeStalePlaintextPartials(root string, cutoff time.Time) error {
	partials, err := plaintextArchives(filepath.Join(root, backupDir), partialSuffix)
	if err != nil {
		return err
	}
	results := make([]error, 0, len(partials))
	for _, partial := range partials {
		results = append(results, removeIfOlder(partial, cutoff))
	}
	return errors.Join(results...)
}

func isWorkDir(name string) bool {
	return strings.HasPrefix(name, workDirPrefix) || strings.HasPrefix(name, restoreDirPrefix)
}

func removeIfOlder(path string, cutoff time.Time) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !info.ModTime().Before(cutoff) {
		return err
	}
	return os.RemoveAll(path)
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
