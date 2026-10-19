package database

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
)

func backupWorld(t *testing.T) (client.Client, *fakeS3, BackupStore) {
	t.Helper()
	c := startEnv(t)
	ensureNamespace(t, c, objectstorage.StoreNamespace)
	ensurePlatformNamespace(t, c)
	deleteBackupIdentity(t, c)
	t.Cleanup(func() { deleteBackupIdentity(t, c) })
	fake, store := newFakeS3(t)
	return c, fake, store
}

func TestBackupStoreIsMadeOnceAndCopiesAllFourKeys(t *testing.T) {
	c, fake, store := backupWorld(t)
	ctx := context.Background()

	ready, err := EnsureBackupStore(ctx, c, store, "bedrock-database-backups")
	if err != nil || ready || fake.createCount() != 0 {
		t.Fatalf("before the Rook user is ready: ready %v err %v creates %d", ready, err, fake.createCount())
	}
	makeBackupUserReady(t, c, "AK", "SK")
	for i := 0; i < 3; i++ {
		ready, err = EnsureBackupStore(ctx, c, store, "bedrock-database-backups")
		if err != nil || !ready {
			t.Fatalf("round %d: ready %v err %v", i, ready, err)
		}
	}
	if fake.createCount() != 1 || fake.created[0] != "bedrock-database-backups" {
		t.Fatalf("bucket creates %v", fake.created)
	}
	var secret corev1.Secret
	must(t, c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: BackupSecretName}, &secret))
	want := map[string]string{"ACCESS_KEY_ID": "AK", "ACCESS_SECRET_KEY": "SK", "AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": "SK"}
	if len(secret.Data) != len(want) {
		t.Fatalf("keys %v", secret.Data)
	}
	for key, value := range want {
		if string(secret.Data[key]) != value {
			t.Fatalf("%s = %q", key, secret.Data[key])
		}
	}

	if _, err := EnsureBackupStore(ctx, c, store, "other-backups"); err != nil || fake.createCount() != 2 {
		t.Fatalf("a new bucket name must make a new bucket: err %v creates %v", err, fake.created)
	}
}

func TestBackupStoreAcceptsABucketThatTheUserOwnsAndFailsOnATakenName(t *testing.T) {
	c, fake, store := backupWorld(t)
	ctx := context.Background()
	_, err := EnsureBackupStore(ctx, c, store, "backups")
	must(t, err)
	makeBackupUserReady(t, c, "AK", "SK")

	fake.createAs = "BucketAlreadyOwnedByYou"
	if ready, err := EnsureBackupStore(ctx, c, store, "backups"); err != nil || !ready {
		t.Fatalf("owned bucket: ready %v err %v", ready, err)
	}
	fake.createAs = "BucketAlreadyExists"
	if ready, err := EnsureBackupStore(ctx, c, store, "taken-name"); err == nil || ready {
		t.Fatalf("a taken name was accepted: ready %v err %v", ready, err)
	}
	var secret corev1.Secret
	must(t, c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: BackupSecretName}, &secret))
	if secret.Annotations[annotationBucket] != "backups" {
		t.Fatalf("annotations %v", secret.Annotations)
	}
}

func backupObject(name, cluster, phase, started, stopped string) *unstructured.Unstructured {
	status := map[string]any{"phase": phase}
	if started != "" {
		status["startedAt"] = started
	}
	if stopped != "" {
		status["stoppedAt"] = stopped
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Backup",
		"metadata": map[string]any{"name": name, "namespace": PlatformNamespace},
		"spec":     map[string]any{"cluster": map[string]any{"name": cluster}},
		"status":   status,
	}}
}

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestBackupStatusWithNoBackups(t *testing.T) {
	status := BackupStatusOf(nil, mustTime("2026-10-19T12:00:00Z"), 0, nil)
	if status.LastAt != nil || status.PITRFrom != nil || status.PITRTo != nil || len(status.Items) != 0 || status.SizeBytes != 0 {
		t.Fatalf("status %+v", status)
	}
}

func TestBackupStatusWithTwoCompletedBackupsAndARunningOne(t *testing.T) {
	now := mustTime("2026-10-19T12:00:00Z")
	sampled := metav1.NewTime(mustTime("2026-10-19T11:55:00Z"))
	backups := []*unstructured.Unstructured{
		backupObject("b1", "uid", "completed", "2026-10-17T02:00:00Z", "2026-10-17T02:05:00Z"),
		backupObject("b2", "uid", "completed", "2026-10-18T02:00:00Z", "2026-10-18T02:06:00Z"),
		backupObject("b3", "uid", "running", "2026-10-19T02:00:00Z", ""),
	}
	status := BackupStatusOf(backups, now, 4096, &sampled)
	if len(status.Items) != 3 || status.Items[0].ID != "b3" || status.Items[1].ID != "b2" || status.Items[2].ID != "b1" {
		t.Fatalf("items %+v", status.Items)
	}
	if status.Items[0].Phase != "running" || status.Items[0].CompletedAt != nil || status.Items[1].CompletedAt == nil {
		t.Fatalf("items %+v", status.Items)
	}
	if !status.PITRFrom.Time.Equal(mustTime("2026-10-17T02:05:00Z")) || !status.LastAt.Time.Equal(mustTime("2026-10-18T02:06:00Z")) || !status.PITRTo.Time.Equal(now) {
		t.Fatalf("window %v %v %v", status.PITRFrom, status.LastAt, status.PITRTo)
	}
	if status.SizeBytes != 4096 || !status.SizeSampledAt.Equal(&sampled) {
		t.Fatalf("size %d %v", status.SizeBytes, status.SizeSampledAt)
	}
}

func TestBackupStatusIgnoresAFailedBackupForTheWindow(t *testing.T) {
	backups := []*unstructured.Unstructured{
		backupObject("good", "uid", "completed", "2026-10-18T02:00:00Z", "2026-10-18T02:05:00Z"),
		backupObject("bad", "uid", "failed", "2026-10-19T02:00:00Z", "2026-10-19T02:01:00Z"),
	}
	status := BackupStatusOf(backups, mustTime("2026-10-19T12:00:00Z"), 0, nil)
	if len(status.Items) != 2 || status.Items[0].ID != "bad" || status.Items[0].Phase != "failed" {
		t.Fatalf("items %+v", status.Items)
	}
	if !status.LastAt.Time.Equal(mustTime("2026-10-18T02:05:00Z")) || !status.PITRFrom.Equal(status.LastAt) {
		t.Fatalf("window %v %v", status.PITRFrom, status.LastAt)
	}
}

func TestBackupStatusKeepsAtMostOneHundredItems(t *testing.T) {
	var backups []*unstructured.Unstructured
	for i := 0; i < 120; i++ {
		backups = append(backups, backupObject("b"+string(rune('a'+i%26))+string(rune('a'+i/26)), "uid", "completed", "2026-10-18T02:00:00Z", "2026-10-18T02:05:00Z"))
	}
	if got := len(BackupStatusOf(backups, time.Now(), 0, nil).Items); got != 100 {
		t.Fatalf("items %d", got)
	}
}

func TestListBackupsKeepsOnlyTheBackupsOfTheCluster(t *testing.T) {
	c := startEnv(t)
	ensurePlatformNamespace(t, c)
	db, _ := storedDatabase(t, c, nil)
	mine := backupObject("mine-"+nextSequence(), string(db.UID), "completed", "2026-10-18T02:00:00Z", "2026-10-18T02:05:00Z")
	other := backupObject("other-"+nextSequence(), "someone-else", "completed", "2026-10-18T02:00:00Z", "2026-10-18T02:05:00Z")
	create(t, c, mine)
	create(t, c, other)
	got, err := ListBackups(context.Background(), c, *db)
	must(t, err)
	if len(got) != 1 || got[0].GetName() != mine.GetName() {
		t.Fatalf("backups %v", got)
	}
}

func TestSizeSampleDueFollowsTheTenMinuteInterval(t *testing.T) {
	now := mustTime("2026-10-19T12:00:00Z")
	at := func(minutes int) *v1alpha1.DatabaseBackupStatus {
		sampled := metav1.NewTime(now.Add(-time.Duration(minutes) * time.Minute))
		return &v1alpha1.DatabaseBackupStatus{SizeSampledAt: &sampled}
	}
	cases := []struct {
		name     string
		previous *v1alpha1.DatabaseBackupStatus
		due      bool
	}{
		{"never", nil, true},
		{"no time", &v1alpha1.DatabaseBackupStatus{}, true},
		{"one minute ago", at(1), false},
		{"nine minutes ago", at(9), false},
		{"ten minutes ago", at(10), true},
		{"an hour ago", at(60), true},
	}
	for _, c := range cases {
		if got := SizeSampleDue(c.previous, now); got != c.due {
			t.Errorf("%s: due=%v", c.name, got)
		}
	}
}

func TestSampleBackupSizeListsThePrefixAtMostEveryTenMinutes(t *testing.T) {
	c, fake, store := backupWorld(t)
	ctx := context.Background()
	_, err := EnsureBackupStore(ctx, c, store, "backups")
	must(t, err)
	makeBackupUserReady(t, c, "AK", "SK")
	_, err = EnsureBackupStore(ctx, c, store, "backups")
	must(t, err)
	db, _ := storedDatabase(t, c, nil)
	uid := string(db.UID)
	fake.put(uid+"/base/1", 1000)
	fake.put(uid+"/wals/1", 234)
	fake.put("other-uid/base/1", 99999)
	cfg, _ := ParseConfig(map[string]string{"database.backup.bucket": "backups"})
	now := mustTime("2026-10-19T12:00:00Z")

	size, sampled, err := SampleBackupSize(ctx, c, store, cfg, *db, now)
	if err != nil || size != 1234 || !sampled.Time.Equal(now) || fake.listCount() != 1 {
		t.Fatalf("first sample: size %d at %v err %v lists %d", size, sampled, err, fake.listCount())
	}
	db.Status.Backup = &v1alpha1.DatabaseBackupStatus{SizeBytes: size, SizeSampledAt: sampled}
	fake.put(uid+"/wals/2", 100)

	size, sampled, err = SampleBackupSize(ctx, c, store, cfg, *db, now.Add(9*time.Minute))
	if err != nil || size != 1234 || !sampled.Time.Equal(now) || fake.listCount() != 1 {
		t.Fatalf("within the interval: size %d err %v lists %d", size, err, fake.listCount())
	}
	size, sampled, err = SampleBackupSize(ctx, c, store, cfg, *db, now.Add(10*time.Minute))
	if err != nil || size != 1334 || !sampled.Time.Equal(now.Add(10*time.Minute)) || fake.listCount() != 2 {
		t.Fatalf("after the interval: size %d at %v err %v lists %d", size, sampled, err, fake.listCount())
	}

	fake.failList = true
	db.Status.Backup = &v1alpha1.DatabaseBackupStatus{SizeBytes: size, SizeSampledAt: sampled}
	keptSize, keptAt, err := SampleBackupSize(ctx, c, store, cfg, *db, now.Add(30*time.Minute))
	if err == nil || keptSize != 1334 || !keptAt.Equal(sampled) {
		t.Fatalf("failed sample: size %d at %v err %v", keptSize, keptAt, err)
	}
}

func TestBackupSpecOfCarriesTheSettingsAndTheSecret(t *testing.T) {
	cfg, _ := ParseConfig(map[string]string{"database.backup.bucket": "my-backups", "database.backup.retention-days": "14"})
	spec := BackupSpecOf(cfg, BackupStore{Endpoint: "http://rgw", Region: "us-east-1"}, true)
	if !spec.Enabled || spec.Bucket != "my-backups" || spec.Endpoint != "http://rgw" || spec.Region != "us-east-1" || spec.RetentionDays != 14 || spec.SecretName != "database-backups-s3" {
		t.Fatalf("spec %+v", spec)
	}
	if BackupSpecOf(cfg, BackupStore{}, false).Enabled {
		t.Fatal("enabled")
	}
}
