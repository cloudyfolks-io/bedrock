package backup

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const maxListedBackups = 50

type Summary struct {
	Phase     string
	Last      time.Time
	Completed []BackupInfo
}

func PhaseOf(veleroPhase string) string {
	switch veleroPhase {
	case phaseCompleted:
		return v1alpha1.BackupPhaseCompleted
	case "Failed", "PartiallyFailed", "FailedValidation":
		return v1alpha1.BackupPhaseFailed
	default:
		return v1alpha1.BackupPhaseInProgress
	}
}

func Summarize(infos []BackupInfo) Summary {
	if len(infos) == 0 {
		return Summary{Phase: v1alpha1.BackupPhasePending}
	}
	completed := completedNewestFirst(infos)
	summary := Summary{Phase: phaseOfNewest(infos), Completed: completed[:min(len(completed), maxListedBackups)]}
	if len(completed) > 0 {
		summary.Last = completed[0].Time
	}
	return summary
}

func phaseOfNewest(infos []BackupInfo) string {
	newest := infos[0]
	for _, info := range infos {
		if PhaseOf(info.Phase) == v1alpha1.BackupPhaseInProgress {
			return v1alpha1.BackupPhaseInProgress
		}
		if compareNewestFirst(info, newest) < 0 {
			newest = info
		}
	}
	return PhaseOf(newest.Phase)
}

func InfoOf(backup *unstructured.Unstructured, bytes int64) BackupInfo {
	phase, _, _ := unstructured.NestedString(backup.Object, "status", "phase")
	stamp, _, _ := unstructured.NestedString(backup.Object, "status", "completionTimestamp")
	completedAt, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		completedAt = backup.GetCreationTimestamp().Time
	}
	return BackupInfo{ID: backup.GetName(), Time: completedAt.UTC(), Phase: phase, Bytes: bytes}
}
