package database

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var requestedAt = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

var planNow = requestedAt.Add(2 * time.Hour)

func requesting(at time.Time, previous *v1alpha1.DatabaseBackupRequest, edit func(*v1alpha1.Database)) v1alpha1.Database {
	db := goldenDatabase()
	db.UID = "uid-1"
	db.Annotations = map[string]string{v1alpha1.AnnotationBackupRequested: at.Format(time.RFC3339)}
	if previous != nil {
		db.Status.Backup = &v1alpha1.DatabaseBackupStatus{Request: previous}
	}
	if edit != nil {
		edit(&db)
	}
	return db
}

func backupRequestOf(at time.Time, phase, id string) *v1alpha1.DatabaseBackupRequest {
	stamp := metav1.NewTime(at)
	return &v1alpha1.DatabaseBackupRequest{At: &stamp, Phase: phase, BackupID: id}
}

func TestNoAnnotationNoBackupRequest(t *testing.T) {
	db := goldenDatabase()
	plan := PlanBackupRequest(db, nil, true, planNow)
	if plan.Status != nil || plan.Create != "" {
		t.Fatalf("plan %+v", plan)
	}
	db.Status.Backup = &v1alpha1.DatabaseBackupStatus{Request: backupRequestOf(requestedAt, v1alpha1.DatabaseBackupRequestCompleted, "x")}
	if plan := PlanBackupRequest(db, nil, true, planNow); plan.Status == nil || plan.Status.Phase != v1alpha1.DatabaseBackupRequestCompleted {
		t.Fatalf("the last result is lost: %+v", plan)
	}
}

func TestANewRequestMakesOneBackupWhenTheEngineIsReady(t *testing.T) {
	plan := PlanBackupRequest(requesting(requestedAt, nil, nil), nil, true, planNow)
	want := "uid-1-req-" + "1792573200"
	if plan.Create != want || plan.Status.Phase != v1alpha1.DatabaseBackupRequestPending || plan.Status.BackupID != want || !plan.Status.At.Time.Equal(requestedAt) {
		t.Fatalf("plan %+v status %+v", plan, plan.Status)
	}
}

func TestARequestWaitsForTheEngineAndThenStarts(t *testing.T) {
	waiting := PlanBackupRequest(requesting(requestedAt, nil, nil), nil, false, planNow)
	if waiting.Create != "" || waiting.Status.Phase != v1alpha1.DatabaseBackupRequestPending {
		t.Fatalf("waiting %+v", waiting)
	}
	again := PlanBackupRequest(requesting(requestedAt, waiting.Status, nil), nil, true, planNow)
	if again.Create != waiting.Status.BackupID {
		t.Fatalf("again %+v", again)
	}
}

func TestARunningRequestFollowsItsBackup(t *testing.T) {
	name := "uid-1-req-1"
	running := backupRequestOf(requestedAt, v1alpha1.DatabaseBackupRequestRunning, name)
	done := requestedAt.Add(time.Minute)
	cases := map[string]struct {
		backups []*unstructured.Unstructured
		want    string
	}{
		"completed": {[]*unstructured.Unstructured{cnpgBackupObject(name, "completed", &done, "stoppedAt")}, v1alpha1.DatabaseBackupRequestCompleted},
		"failed":    {[]*unstructured.Unstructured{cnpgBackupObject(name, "failed", nil, "")}, v1alpha1.DatabaseBackupRequestFailed},
		"running":   {[]*unstructured.Unstructured{cnpgBackupObject(name, "running", nil, "")}, v1alpha1.DatabaseBackupRequestRunning},
		"unseen":    {nil, v1alpha1.DatabaseBackupRequestRunning},
	}
	for label, tc := range cases {
		plan := PlanBackupRequest(requesting(requestedAt, running, nil), tc.backups, true, planNow)
		if plan.Create != "" || plan.Status.Phase != tc.want {
			t.Errorf("%s: %+v", label, plan.Status)
		}
	}
}

func TestAnOlderOrEqualTimeStartsNoSecondBackup(t *testing.T) {
	last := backupRequestOf(requestedAt, v1alpha1.DatabaseBackupRequestCompleted, "uid-1-req-1")
	for _, at := range []time.Time{requestedAt, requestedAt.Add(-time.Hour)} {
		plan := PlanBackupRequest(requesting(at, last, nil), nil, true, planNow)
		if plan.Create != "" || plan.Status.Phase != v1alpha1.DatabaseBackupRequestCompleted {
			t.Errorf("%s: %+v", at, plan)
		}
	}
	plan := PlanBackupRequest(requesting(requestedAt.Add(time.Hour), last, nil), nil, true, planNow)
	if plan.Create == "" || plan.Status.BackupID != plan.Create {
		t.Fatalf("a newer time did not start: %+v", plan)
	}
}

func TestARequestIsRefusedWithAReason(t *testing.T) {
	cases := map[string]struct {
		edit   func(*v1alpha1.Database)
		reason string
	}{
		"valkey": {func(db *v1alpha1.Database) {
			db.Spec.Engine = v1alpha1.DatabaseEngineValkey
			db.Spec.Backup.Enabled = false
		}, ReasonBackupNotSupported},
		"disable": {func(db *v1alpha1.Database) { db.Spec.Backup.Enabled = false }, ReasonBackupsDisabled},
		"garbage": {func(db *v1alpha1.Database) { db.Annotations[v1alpha1.AnnotationBackupRequested] = "yesterday" }, ReasonInvalidBackupRequest},
	}
	for label, tc := range cases {
		plan := PlanBackupRequest(requesting(requestedAt, nil, tc.edit), nil, true, planNow)
		if plan.Create != "" || plan.Status.Phase != v1alpha1.DatabaseBackupRequestRefused || plan.Status.Reason != tc.reason || plan.Status.Message == "" {
			t.Errorf("%s: %+v", label, plan.Status)
		}
	}
}

func TestARequestIsRefusedWhileAnEarlierRequestedBackupRuns(t *testing.T) {
	last := backupRequestOf(requestedAt, v1alpha1.DatabaseBackupRequestRunning, "uid-1-req-1")
	for label, tc := range map[string]struct {
		backups []*unstructured.Unstructured
		refused bool
	}{
		"running":   {[]*unstructured.Unstructured{cnpgBackupObject("uid-1-req-1", "running", nil, "")}, true},
		"started":   {[]*unstructured.Unstructured{cnpgBackupObject("uid-1-req-1", "started", nil, "")}, true},
		"completed": {[]*unstructured.Unstructured{cnpgBackupObject("uid-1-req-1", "completed", &planNow, "stoppedAt")}, false},
		"failed":    {[]*unstructured.Unstructured{cnpgBackupObject("uid-1-req-1", "failed", nil, "")}, false},
		"other":     {[]*unstructured.Unstructured{cnpgBackupObject("uid-1-pre-upgrade", "running", nil, "")}, false},
	} {
		plan := PlanBackupRequest(requesting(requestedAt.Add(time.Hour), last, nil), tc.backups, true, planNow)
		if tc.refused && (plan.Create != "" || plan.Status.Phase != v1alpha1.DatabaseBackupRequestRefused || plan.Status.Reason != ReasonBackupInProgress) {
			t.Errorf("%s: %+v", label, plan)
		}
		if !tc.refused && plan.Create == "" {
			t.Errorf("%s: no backup started: %+v", label, plan)
		}
	}
}

func TestARequestMoreThanFiveMinutesAheadIsRefusedAndBlocksNothing(t *testing.T) {
	last := backupRequestOf(requestedAt, v1alpha1.DatabaseBackupRequestCompleted, "uid-1-req-1")
	far := PlanBackupRequest(requesting(planNow.Add(6*time.Minute), last, nil), nil, true, planNow)
	if far.Create != "" || far.Status.Phase != v1alpha1.DatabaseBackupRequestRefused || far.Status.Reason != ReasonBackupRequestInFuture || !far.Status.At.Time.Equal(requestedAt) {
		t.Fatalf("far %+v", far.Status)
	}
	near := PlanBackupRequest(requesting(planNow.Add(4*time.Minute), last, nil), nil, true, planNow)
	if near.Create == "" {
		t.Fatalf("near %+v", near.Status)
	}
}

func (w *world) requestBackup(at time.Time) {
	w.t.Helper()
	ctx := context.Background()
	must(w.t, w.c.Get(ctx, client.ObjectKeyFromObject(w.db), w.db))
	if w.db.Annotations == nil {
		w.db.Annotations = map[string]string{}
	}
	w.db.Annotations[v1alpha1.AnnotationBackupRequested] = at.UTC().Format(time.RFC3339)
	must(w.t, w.c.Update(ctx, w.db))
}

func (w *world) requestedBackups() []*unstructured.Unstructured {
	w.t.Helper()
	all, err := ListBackups(context.Background(), w.c, *w.db)
	must(w.t, err)
	var found []*unstructured.Unstructured
	for _, backup := range all {
		if strings.Contains(backup.GetName(), "-req-") {
			found = append(found, backup)
		}
	}
	return found
}

func TestARequestedBackupRunsOnceAndTheResultShowsInTheStatus(t *testing.T) {
	w := newWorld(t, nil)
	w.bringUp()
	w.requestBackup(w.clock.Add(-time.Minute))
	w.reconcile()
	w.reconcile()
	created := w.requestedBackups()
	if len(created) != 1 || created[0].GetKind() != "Backup" || backupClusterOf(created[0]) != string(w.db.UID) {
		t.Fatalf("backups %v", created)
	}
	request := w.db.Status.Backup.Request
	if request == nil || request.Phase != v1alpha1.DatabaseBackupRequestRunning || request.BackupID != created[0].GetName() {
		t.Fatalf("request %+v", request)
	}
	must(t, unstructured.SetNestedField(created[0].Object, "completed", "status", "phase"))
	must(t, unstructured.SetNestedField(created[0].Object, w.clock.UTC().Format(time.RFC3339), "status", "stoppedAt"))
	must(t, w.c.Status().Update(context.Background(), created[0]))
	w.reconcile()
	w.reconcile()
	if w.db.Status.Backup.Request.Phase != v1alpha1.DatabaseBackupRequestCompleted || len(w.requestedBackups()) != 1 {
		t.Fatalf("request %+v backups %d", w.db.Status.Backup.Request, len(w.requestedBackups()))
	}
}

func TestABackupRequestOnABackupOffDatabaseIsRefusedWithoutAnObject(t *testing.T) {
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) { spec.Backup.Enabled = false })
	w.bringUp()
	w.requestBackup(w.clock)
	w.reconcile()
	w.reconcile()
	if w.db.Status.Backup == nil || w.db.Status.Backup.Request == nil || w.db.Status.Backup.Request.Reason != ReasonBackupsDisabled || len(w.requestedBackups()) != 0 {
		t.Fatalf("status %+v", w.db.Status.Backup)
	}
}

func TestABackupRequestOnValkeyIsRefused(t *testing.T) {
	w := valkeyWorld(t)
	w.bringUpValkey()
	w.requestBackup(w.clock)
	w.reconcile()
	w.reconcile()
	if w.db.Status.Backup == nil || w.db.Status.Backup.Request == nil || w.db.Status.Backup.Request.Reason != ReasonBackupNotSupported {
		t.Fatalf("status %+v", w.db.Status.Backup)
	}
}
