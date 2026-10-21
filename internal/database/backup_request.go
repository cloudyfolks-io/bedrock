package database

import (
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	ReasonBackupNotSupported   = "BackupNotSupported"
	ReasonInvalidBackupRequest = "InvalidBackupRequest"

	annotationBackupRequestedAt = "bedrock.cloudyfolks.io/requested-at"
)

type BackupRequestPlan struct {
	Status *v1alpha1.DatabaseBackupRequest
	Create string
}

func RequestedBackupName(name string, at time.Time) string {
	return name + "-req-" + strconv.FormatInt(at.Unix(), 10)
}

func previousBackupRequest(db v1alpha1.Database) *v1alpha1.DatabaseBackupRequest {
	if db.Status.Backup == nil {
		return nil
	}
	return db.Status.Backup.Request
}

func refusedRequest(at *metav1.Time, reason, message string) *v1alpha1.DatabaseBackupRequest {
	return &v1alpha1.DatabaseBackupRequest{At: at, Phase: v1alpha1.DatabaseBackupRequestRefused, Reason: reason, Message: message}
}

func backupRequestRefusal(db v1alpha1.Database, at *metav1.Time) *v1alpha1.DatabaseBackupRequest {
	switch {
	case db.Spec.Engine == v1alpha1.DatabaseEngineValkey:
		return refusedRequest(at, ReasonBackupNotSupported, "a Valkey database has no backups")
	case !db.Spec.Backup.Enabled:
		return refusedRequest(at, ReasonBackupsDisabled, "backups are off for this database: set backup.enabled to true")
	}
	return nil
}

func PlanBackupRequest(db v1alpha1.Database, backups []*unstructured.Unstructured, ready bool) BackupRequestPlan {
	previous := previousBackupRequest(db)
	raw := db.Annotations[v1alpha1.AnnotationBackupRequested]
	if raw == "" {
		return BackupRequestPlan{Status: previous}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		var kept *metav1.Time
		if previous != nil {
			kept = previous.At
		}
		return BackupRequestPlan{Status: refusedRequest(kept, ReasonInvalidBackupRequest, fmt.Sprintf("the annotation %s must hold an RFC 3339 time", v1alpha1.AnnotationBackupRequested))}
	}
	at := metav1.NewTime(parsed.UTC())
	if previous != nil && previous.At != nil {
		switch {
		case at.Time.Before(previous.At.Time):
			return BackupRequestPlan{Status: previous}
		case at.Time.Equal(previous.At.Time):
			return continueRequest(db, *previous, backups, ready)
		}
	}
	if refusal := backupRequestRefusal(db, &at); refusal != nil {
		return BackupRequestPlan{Status: refusal}
	}
	name := RequestedBackupName(Name(db.UID), at.Time)
	plan := BackupRequestPlan{Status: &v1alpha1.DatabaseBackupRequest{At: &at, Phase: v1alpha1.DatabaseBackupRequestPending, BackupID: name}}
	if ready {
		plan.Create = name
	}
	return plan
}

func continueRequest(db v1alpha1.Database, previous v1alpha1.DatabaseBackupRequest, backups []*unstructured.Unstructured, ready bool) BackupRequestPlan {
	switch previous.Phase {
	case v1alpha1.DatabaseBackupRequestPending:
		plan := BackupRequestPlan{Status: &previous}
		if ready {
			plan.Create = previous.BackupID
		}
		return plan
	case v1alpha1.DatabaseBackupRequestRunning:
		return BackupRequestPlan{Status: followBackup(db, previous, backups)}
	}
	return BackupRequestPlan{Status: &previous}
}

func followBackup(db v1alpha1.Database, request v1alpha1.DatabaseBackupRequest, backups []*unstructured.Unstructured) *v1alpha1.DatabaseBackupRequest {
	state := BackupStateOf(db.Spec.Engine)
	for _, backup := range backups {
		if backup.GetName() != request.BackupID {
			continue
		}
		completed, failed := state(backup)
		switch {
		case failed:
			request.Phase = v1alpha1.DatabaseBackupRequestFailed
		case completed != nil:
			request.Phase = v1alpha1.DatabaseBackupRequestCompleted
		}
	}
	return &request
}
