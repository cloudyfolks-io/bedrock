package backup

import (
	"slices"
	"strings"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type BackupInfo struct {
	ID    string
	Time  time.Time
	Phase string
	Bytes int64
}

const phaseCompleted = "Completed"

func compareNewestFirst(a, b BackupInfo) int {
	if byTime := b.Time.Compare(a.Time); byTime != 0 {
		return byTime
	}
	return strings.Compare(b.ID, a.ID)
}

func completedNewestFirst(infos []BackupInfo) []BackupInfo {
	var done []BackupInfo
	for _, info := range infos {
		if info.Phase == phaseCompleted {
			done = append(done, info)
		}
	}
	slices.SortFunc(done, compareNewestFirst)
	return done
}

func Prune(infos []BackupInfo, keep int) []string {
	done := completedNewestFirst(infos)
	if len(done) <= keep {
		return nil
	}
	var ids []string
	for i := len(done) - 1; i >= keep; i-- {
		ids = append(ids, done[i].ID)
	}
	return ids
}

func pinnedBackupID(restore v1alpha1.VirtualMachineRestore) string {
	if restore.Spec.BackupID != "" {
		return restore.Spec.BackupID
	}
	return restore.Status.BackupID
}

func Unpinned(ids []string, restores []v1alpha1.VirtualMachineRestore) []string {
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool {
		return slices.ContainsFunc(restores, func(restore v1alpha1.VirtualMachineRestore) bool {
			return pinnedBackupID(restore) == id
		})
	})
}
