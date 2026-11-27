package database

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
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
	status := BackupStatusOf(nil, 0, nil)
	if status.LastAt != nil || status.PITRFrom != nil || status.PITRTo != nil || len(status.Items) != 0 || status.SizeBytes != 0 {
		t.Fatalf("status %+v", status)
	}
}

func TestBackupStatusWithTwoCompletedBackupsAndARunningOne(t *testing.T) {
	sampled := metav1.NewTime(mustTime("2026-10-19T11:55:00Z"))
	backups := []*unstructured.Unstructured{
		backupObject("b1", "uid", "completed", "2026-10-17T02:00:00Z", "2026-10-17T02:05:00Z"),
		backupObject("b2", "uid", "completed", "2026-10-18T02:00:00Z", "2026-10-18T02:06:00Z"),
		backupObject("b3", "uid", "running", "2026-10-19T02:00:00Z", ""),
	}
	status := BackupStatusOf(backups, 4096, &sampled)
	if len(status.Items) != 3 || status.Items[0].ID != "b3" || status.Items[1].ID != "b2" || status.Items[2].ID != "b1" {
		t.Fatalf("items %+v", status.Items)
	}
	if status.Items[0].Phase != "running" || status.Items[0].CompletedAt != nil || status.Items[1].CompletedAt == nil {
		t.Fatalf("items %+v", status.Items)
	}
	if status.LastAt == nil || !status.LastAt.Time.Equal(mustTime("2026-10-18T02:06:00Z")) ||
		status.PITRFrom != nil || status.PITRTo != nil {
		t.Fatalf("status %+v", status)
	}
	if status.SizeBytes != 4096 || !status.SizeSampledAt.Equal(&sampled) {
		t.Fatalf("size %d %v", status.SizeBytes, status.SizeSampledAt)
	}
}

func TestBackupStatusIgnoresAFailedBackupForLastAt(t *testing.T) {
	backups := []*unstructured.Unstructured{
		backupObject("good", "uid", "completed", "2026-10-18T02:00:00Z", "2026-10-18T02:05:00Z"),
		backupObject("bad", "uid", "failed", "2026-10-19T02:00:00Z", "2026-10-19T02:01:00Z"),
	}
	status := BackupStatusOf(backups, 0, nil)
	if len(status.Items) != 2 || status.Items[0].ID != "bad" || status.Items[0].Phase != "failed" {
		t.Fatalf("items %+v", status.Items)
	}
	if status.LastAt == nil || !status.LastAt.Time.Equal(mustTime("2026-10-18T02:05:00Z")) {
		t.Fatalf("last backup %v", status.LastAt)
	}
}

func TestBackupStatusKeepsAtMostOneHundredItems(t *testing.T) {
	var backups []*unstructured.Unstructured
	for i := 0; i < 120; i++ {
		backups = append(backups, backupObject("b"+string(rune('a'+i%26))+string(rune('a'+i/26)), "uid", "completed", "2026-10-18T02:00:00Z", "2026-10-18T02:05:00Z"))
	}
	if got := len(BackupStatusOf(backups, 0, nil).Items); got != 100 {
		t.Fatalf("items %d", got)
	}
}

func TestMySQLBackupStatusKeepsCompletedBackupWithoutAPITRWindow(t *testing.T) {
	completed := mustTime("2026-10-18T02:05:00Z")
	backup := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ps.percona.com/v1", "kind": "PerconaServerMySQLBackup",
		"metadata": map[string]any{"name": "mysql-backup", "creationTimestamp": "2026-10-18T02:00:00Z"},
		"status":   map[string]any{"state": "Succeeded", "completed": completed.Format(time.RFC3339)},
	}}
	status := BackupStatusOf([]*unstructured.Unstructured{backup}, 4096, nil)
	if status.PITRFrom != nil || status.PITRTo != nil {
		t.Fatalf("MySQL PITR window = %v to %v", status.PITRFrom, status.PITRTo)
	}
	if status.LastAt == nil || !status.LastAt.Time.Equal(completed) || status.SizeBytes != 4096 || len(status.Items) != 1 ||
		status.Items[0].ID != "mysql-backup" || status.Items[0].Phase != backupPhaseCompleted {
		t.Fatalf("status %+v", status)
	}
}

type failingBackupList struct{ client.Reader }

func (failingBackupList) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("the backup list failed")
}

func TestBackupStatusClearsAnOldWindowWhenTheListFails(t *testing.T) {
	last := metav1.NewTime(mustTime("2026-10-18T02:05:00Z"))
	previous := &v1alpha1.DatabaseBackupStatus{LastAt: &last, PITRFrom: &last, PITRTo: &last, SizeBytes: 4096}
	for _, engine := range []string{v1alpha1.DatabaseEnginePostgreSQL, v1alpha1.DatabaseEngineMySQL} {
		t.Run(engine, func(t *testing.T) {
			db := &v1alpha1.Database{Spec: v1alpha1.DatabaseSpec{Engine: engine}, Status: v1alpha1.DatabaseStatus{Backup: previous}}
			r := Reconciler{Live: failingBackupList{}}
			status := r.observeBackup(t.Context(), &flow{db: db}, true)
			if status.PITRFrom != nil || status.PITRTo != nil || !status.LastAt.Equal(&last) || status.SizeBytes != 4096 {
				t.Fatalf("status %+v", status)
			}
		})
	}
	if previous.PITRFrom == nil || previous.PITRTo == nil {
		t.Fatal("the backup observer changed its input")
	}
}

type recoveryWindowReader struct {
	client.Reader
	store *unstructured.Unstructured
}

func (r recoveryWindowReader) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	if r.store == nil {
		return errors.New("the backup store read failed")
	}
	target, ok := obj.(*unstructured.Unstructured)
	if !ok || target.GroupVersionKind() != ObjectStoreGVK || key != client.ObjectKeyFromObject(r.store) {
		return errors.New("the backup store key differs")
	}
	*target = *r.store.DeepCopy()
	return nil
}

func TestNativePostgresRecoveryWindow(t *testing.T) {
	created := mustTime("2026-10-17T00:00:00Z")
	from := mustTime("2026-10-17T02:00:00Z")
	to := mustTime("2026-10-18T02:00:00Z")
	now := mustTime("2026-10-18T12:00:00Z")
	last := metav1.NewTime(to)
	project := Project{Namespace: "test-project", Tenant: "test", Name: "project"}
	db := v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: project.Namespace, UID: "postgres-uid", CreationTimestamp: metav1.NewTime(created)},
		Spec:       v1alpha1.DatabaseSpec{Engine: v1alpha1.DatabaseEnginePostgreSQL},
	}
	spec := engine.BackupSpec{Enabled: true, Bucket: "backups", Endpoint: "http://rgw", SecretName: BackupSecretName}
	store := Adopt(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": ObjectStoreGVK.GroupVersion().String(),
		"kind":       ObjectStoreGVK.Kind,
		"metadata":   map[string]any{"name": Name(db.UID), "namespace": PlatformNamespace},
		"spec": map[string]any{"configuration": map[string]any{
			"destinationPath": "s3://backups/postgres-uid/",
			"endpointURL":     spec.Endpoint,
			"s3Credentials": map[string]any{
				"accessKeyId":     map[string]any{"name": spec.SecretName, "key": "ACCESS_KEY_ID"},
				"secretAccessKey": map[string]any{"name": spec.SecretName, "key": "ACCESS_SECRET_KEY"},
			},
		}},
		"status": map[string]any{"serverRecoveryWindow": map[string]any{
			string(db.UID): map[string]any{
				"firstRecoverabilityPoint": from.Format(time.RFC3339),
				"lastSuccessfulBackupTime": to.Format(time.RFC3339),
			},
			"another-server": map[string]any{
				"firstRecoverabilityPoint": created.Format(time.RFC3339),
				"lastSuccessfulBackupTime": now.Format(time.RFC3339),
			},
		}},
	}}, db, project)
	store.SetUID("object-store-uid")
	store.SetGeneration(1)
	store.SetCreationTimestamp(metav1.NewTime(created))

	gotFrom, gotTo := nativeRecoveryWindow(t.Context(), recoveryWindowReader{store: store}, db, project, spec, &last, now)
	if gotFrom == nil || gotTo == nil || !gotFrom.Time.Equal(from) || !gotTo.Time.Equal(to) {
		t.Fatalf("recovery window %v to %v, want %v to %v", gotFrom, gotTo, from, to)
	}
	gotFrom, gotTo = nativeRecoveryWindow(t.Context(), recoveryWindowReader{store: store}, db, project, spec, nil, now)
	if gotFrom == nil || gotTo == nil || !gotFrom.Time.Equal(from) || !gotTo.Time.Equal(to) {
		t.Fatalf("recovery window without Backup records %v to %v, want %v to %v", gotFrom, gotTo, from, to)
	}
	configuration := []string{"spec", "configuration"}
	window := []string{"status", "serverRecoveryWindow", string(db.UID)}
	for _, bounds := range []struct{ first, last, from, to string }{
		{"02:00:00.1234560000", "02:00:00.123458", "02:00:00.123456", "02:00:00.123458"},
		{"02:00:00.1234560001", "02:00:00.1234589", "02:00:00.123457", "02:00:00.123458"},
		{"02:00:00.1234560001", "02:00:00.1234569999", "", ""},
	} {
		candidate := store.DeepCopy()
		must(t, unstructured.SetNestedField(candidate.Object, "2026-10-18T"+bounds.first+"Z", append(window, "firstRecoverabilityPoint")...))
		must(t, unstructured.SetNestedField(candidate.Object, "2026-10-18T"+bounds.last+"Z", append(window, "lastSuccessfulBackupTime")...))
		start, end := nativeRecoveryWindow(t.Context(), recoveryWindowReader{store: candidate}, db, project, spec, nil, now)
		if bounds.from == "" {
			if start != nil || end != nil {
				t.Fatalf("a window without an exact microsecond advertised %v to %v", start, end)
			}
			continue
		}
		if start == nil || end == nil || start.Format("15:04:05.000000") != bounds.from || end.Format("15:04:05.000000") != bounds.to {
			t.Fatalf("unexpected microsecond bounds for %v: %v to %v", bounds, start, end)
		}
	}
	cases := []struct {
		name  string
		path  []string
		value any
	}{
		{"foreign owner", []string{"metadata", "annotations", platform.AnnotationOwnerUID}, "foreign-uid"},
		{"foreign prefix", append(configuration, "destinationPath"), "s3://backups/previous-uid/"},
		{"foreign endpoint", append(configuration, "endpointURL"), "https://other-rgw"},
		{"wrong access key", append(configuration, "s3Credentials", "accessKeyId", "key"), "AWS_ACCESS_KEY_ID"},
		{"wrong secret key", append(configuration, "s3Credentials", "secretAccessKey", "key"), "AWS_SECRET_ACCESS_KEY"},
		{"missing access key", append(configuration, "s3Credentials", "accessKeyId", "key"), nil},
		{"missing server", window, nil},
		{"invalid start", append(window, "firstRecoverabilityPoint"), "invalid"},
		{"reversed window", append(window, "firstRecoverabilityPoint"), to.Add(time.Second).Format(time.RFC3339)},
		{"future checkpoint", append(window, "lastSuccessfulBackupTime"), now.Add(time.Second).Format(time.RFC3339)},
		{"stale checkpoint", append(window, "lastSuccessfulBackupTime"), to.Add(-time.Second).Format(time.RFC3339)},
		{"replaced store", []string{"metadata", "creationTimestamp"}, to.Add(time.Second).Format(time.RFC3339)},
		{"store deleting", []string{"metadata", "deletionTimestamp"}, now.Format(time.RFC3339)},
		{"store revision without write evidence", []string{"metadata", "generation"}, int64(2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := store.DeepCopy()
			if tc.value == nil {
				unstructured.RemoveNestedField(candidate.Object, tc.path...)
			} else {
				must(t, unstructured.SetNestedField(candidate.Object, tc.value, tc.path...))
			}
			start, end := nativeRecoveryWindow(t.Context(), recoveryWindowReader{store: candidate}, db, project, spec, &last, now)
			if start != nil || end != nil {
				t.Fatalf("recovery window %v to %v", start, end)
			}
		})
	}
	if start, end := nativeRecoveryWindow(t.Context(), recoveryWindowReader{}, db, project, spec, &last, now); start != nil || end != nil {
		t.Fatalf("recovery window after failed read %v to %v", start, end)
	}

	store.SetGeneration(2)
	statusWrite := metav1.NewTime(now)
	specWrite := metav1.NewTime(to.Add(-time.Second))
	fields := metav1.NewFieldsV1(`{"f:spec":{}}`)
	store.SetManagedFields([]metav1.ManagedFieldsEntry{
		{Manager: v1alpha1.DatabaseFieldManager, FieldsType: "FieldsV1", Time: &specWrite, FieldsV1: fields},
		{Manager: "barman", Subresource: "status", FieldsType: "FieldsV1", Time: &statusWrite, FieldsV1: fields},
	})
	if start, end := nativeRecoveryWindow(t.Context(), recoveryWindowReader{store: store}, db, project, spec, &last, now); start == nil || end == nil {
		t.Fatalf("recovery window after verified store change %v to %v", start, end)
	}
	entries := store.GetManagedFields()
	entries[0].Time = &statusWrite
	store.SetManagedFields(entries)
	if start, end := nativeRecoveryWindow(t.Context(), recoveryWindowReader{store: store}, db, project, spec, &last, now); start != nil || end != nil {
		t.Fatalf("recovery window before the current store revision %v to %v", start, end)
	}
}

func TestListBackupsKeepsOnlyTheBackupsOfTheCluster(t *testing.T) {
	c := startEnv(t)
	ensurePlatformNamespace(t, c)
	db, _ := storedDatabase(t, c, nil)
	mine := backupObject("mine-"+nextSequence(), Name(db.UID), "completed", "2026-10-18T02:00:00Z", "2026-10-18T02:05:00Z")
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
	spec := BackupSpecOf(cfg, BackupStore{Endpoint: "http://rgw", Region: "us-east-1"}, v1alpha1.DatabaseBackupSpec{Enabled: true})
	if !spec.Enabled || spec.Bucket != "my-backups" || spec.Endpoint != "http://rgw" || spec.Region != "us-east-1" || spec.RetentionDays != 14 || spec.SecretName != "database-backups-s3" {
		t.Fatalf("spec %+v", spec)
	}
	if BackupSpecOf(cfg, BackupStore{}, v1alpha1.DatabaseBackupSpec{}).Enabled {
		t.Fatal("enabled")
	}
}
