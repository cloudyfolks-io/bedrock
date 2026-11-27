package database

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	annotationUpgradeTarget  = "bedrock.cloudyfolks.io/upgrade-target"
	annotationUpgradeRequest = "bedrock.cloudyfolks.io/upgrade-requested-at"
	preBackupRetryAfter      = time.Hour
)

type UpgradeBackup struct {
	Latest    *time.Time
	Requested bool
	FailedAt  *time.Time
}

func requestedAtOf(backup *unstructured.Unstructured) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339, backup.GetAnnotations()[annotationUpgradeRequest])
	return at, err == nil
}

func isPreUpgrade(uid string, backup *unstructured.Unstructured) bool {
	return strings.HasPrefix(backup.GetName(), uid+"-pre-")
}

func UpgradeBackupFacts(uid, target string, now time.Time, maxAge time.Duration, backups []*unstructured.Unstructured, state BackupState) UpgradeBackup {
	var facts UpgradeBackup
	for _, backup := range backups {
		completed, failed := state(backup)
		if isPreUpgrade(uid, backup) {
			requested, recorded := requestedAtOf(backup)
			if !recorded || backup.GetAnnotations()[annotationUpgradeTarget] != target || now.Sub(requested) > maxAge {
				continue
			}
			facts.Requested = facts.Requested || !failed
			if failed && (facts.FailedAt == nil || requested.After(*facts.FailedAt)) {
				facts.FailedAt = &requested
			}
		}
		if completed != nil && (facts.Latest == nil || completed.After(*facts.Latest)) {
			facts.Latest = completed
		}
	}
	return facts
}

func FailedPreUpgradeBackups(uid string, backups []*unstructured.Unstructured, state BackupState) []*unstructured.Unstructured {
	var failed []*unstructured.Unstructured
	for _, backup := range backups {
		if _, isFailed := state(backup); isFailed && isPreUpgrade(uid, backup) {
			failed = append(failed, backup)
		}
	}
	return failed
}
