package backup

import (
	"slices"
	"strings"
	"time"
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
