package database

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func UpgradeBackupFacts(uid string, backups []*unstructured.Unstructured, state BackupState) (*time.Time, bool) {
	var latest *time.Time
	preRequested := false
	for _, backup := range backups {
		completed, failed := state(backup)
		if strings.HasPrefix(backup.GetName(), uid+"-pre-") && !failed {
			preRequested = true
		}
		if completed != nil && (latest == nil || completed.After(*latest)) {
			latest = completed
		}
	}
	return latest, preRequested
}
