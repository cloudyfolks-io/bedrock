package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
)

const retiredMinAge = 24 * time.Hour

type retiredKind struct {
	dir    string
	suffix string
}

func retiredKinds(root string) []retiredKind {
	return []retiredKind{
		{dir: filepath.Join(root, seal.RetiredDir), suffix: ".key"},
		{dir: filepath.Join(root, encryptionDir, encryptionRetired), suffix: encryptionFileStamp},
	}
}

func pruneRetired(root string, now time.Time) error {
	stamps, err := archiveStamps(filepath.Join(root, backupDir))
	if err != nil {
		return err
	}
	var results []error
	for _, kind := range retiredKinds(root) {
		results = append(results, pruneRetiredKind(kind, stamps, now))
	}
	return errors.Join(results...)
}

func pruneRetiredKind(kind retiredKind, stamps []string, now time.Time) error {
	entries, err := os.ReadDir(kind.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var results []error
	for _, entry := range entries {
		stamp, isRetired := strings.CutSuffix(entry.Name(), kind.suffix)
		if isRetired && validStamp(stamp) && !youngerThanOneCycle(stamp, now) && !neededBy(stamp, stamps) {
			results = append(results, removeIfExists(filepath.Join(kind.dir, entry.Name())))
		}
	}
	return errors.Join(results...)
}

func youngerThanOneCycle(stamp string, now time.Time) bool {
	retired, _ := time.Parse(stampLayout, stamp)
	return now.Sub(retired) < retiredMinAge
}

func neededBy(retiredStamp string, stamps []string) bool {
	return slices.ContainsFunc(stamps, func(archive string) bool { return archive <= retiredStamp })
}

func archiveStamps(dir string) ([]string, error) {
	var stamps []string
	for _, prefix := range []string{upgradeBackupPrefix, etcdSnapshotPrefix} {
		for _, tail := range []string{"", partialSuffix} {
			paths, err := archivePaths(dir, prefix, tail)
			if err != nil {
				return nil, err
			}
			for _, path := range paths {
				if stamp := backupStamp(strings.TrimSuffix(path, partialSuffix)); validStamp(stamp) {
					stamps = append(stamps, stamp)
				}
			}
		}
	}
	return stamps, nil
}

func validStamp(stamp string) bool {
	_, err := time.Parse(stampLayout, stamp)
	return err == nil
}
