package database

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func getTombstone(c client.Client, db *v1alpha1.Database) (*corev1.ConfigMap, bool) {
	var tombstone corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: TombstoneName(db.UID)}, &tombstone); err != nil {
		return nil, false
	}
	return &tombstone, true
}

func TestTombstoneIsMadeOnceWithTheRetentionTime(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, nil)
	now := mustTime("2026-10-19T12:00:00Z")
	must(t, EnsureTombstone(ctx, c, *db, project, 168*time.Hour, now))
	must(t, EnsureTombstone(ctx, c, *db, project, 168*time.Hour, now.Add(48*time.Hour)))
	tombstone, found := getTombstone(c, db)
	if !found {
		t.Fatal("no tombstone")
	}
	if tombstone.Name != "tombstone-"+string(db.UID) || tombstone.Annotations[AnnotationDeleteAfter] != "2026-10-26T12:00:00Z" {
		t.Fatalf("tombstone %s %v", tombstone.Name, tombstone.Annotations)
	}
	if tombstone.Data["namespace"] != db.Namespace || tombstone.Labels[v1alpha1.LabelKind] != "Tombstone" || tombstone.Labels[LabelInstance] != "" {
		t.Fatalf("tombstone %v %v", tombstone.Data, tombstone.Labels)
	}
	namespace, found, err := TombstoneNamespace(ctx, c, string(db.UID))
	if err != nil || !found || namespace != db.Namespace {
		t.Fatalf("namespace %q %v %v", namespace, found, err)
	}
	if _, found, _ := TombstoneNamespace(ctx, c, "no-such-uid"); found {
		t.Fatal("found a tombstone that does not exist")
	}
}

func purgeWorld(t *testing.T) (client.Client, *fakeS3, BackupStore, Config) {
	t.Helper()
	c, fake, store := backupWorld(t)
	ctx := context.Background()
	_, err := EnsureBackupStore(ctx, c, store, "backups")
	must(t, err)
	makeBackupUserReady(t, c, "AK", "SK")
	_, err = EnsureBackupStore(ctx, c, store, "backups")
	must(t, err)
	cfg, _ := ParseConfig(map[string]string{"database.backup.bucket": "backups"})
	return c, fake, store, cfg
}

func TestPurgeDeletesThePrefixAndTheTombstoneOnlyAfterTheTime(t *testing.T) {
	c, fake, store, cfg := purgeWorld(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, nil)
	other, otherProject := storedDatabase(t, c, nil)
	now := mustTime("2026-10-19T12:00:00Z")
	uid, otherUID := string(db.UID), string(other.UID)
	fake.put(uid+"/base/1", 10)
	fake.put(uid+"/wals/1", 5)
	fake.put(otherUID+"/base/1", 10)
	must(t, EnsureTombstone(ctx, c, *db, project, 24*time.Hour, now))
	must(t, EnsureTombstone(ctx, c, *other, otherProject, 72*time.Hour, now))

	must(t, PurgeBackups(ctx, c, store, cfg, now.Add(23*time.Hour+59*time.Minute)))
	if !fake.has(uid + "/base/1") {
		t.Fatal("purged before the time")
	}
	if _, found := getTombstone(c, db); !found {
		t.Fatal("tombstone removed before the time")
	}

	must(t, PurgeBackups(ctx, c, store, cfg, now.Add(24*time.Hour)))
	if fake.has(uid+"/base/1") || fake.has(uid+"/wals/1") {
		t.Fatal("the prefix is still there after the time")
	}
	if _, found := getTombstone(c, db); found {
		t.Fatal("the tombstone is still there")
	}
	if !fake.has(otherUID + "/base/1") {
		t.Fatal("the purge removed a prefix of another database")
	}
	if _, found := getTombstone(c, other); !found {
		t.Fatal("the purge removed another tombstone")
	}
}

func TestPurgeFailureKeepsTheTombstoneAndRetries(t *testing.T) {
	c, fake, store, cfg := purgeWorld(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, nil)
	now := mustTime("2026-10-19T12:00:00Z")
	fake.put(string(db.UID)+"/base/1", 10)
	must(t, EnsureTombstone(ctx, c, *db, project, 0, now))

	fake.failDelete = true
	if err := PurgeBackups(ctx, c, store, cfg, now); err == nil {
		t.Fatal("a failed delete was not reported")
	}
	if _, found := getTombstone(c, db); !found || !fake.has(string(db.UID)+"/base/1") {
		t.Fatal("a failed purge dropped the tombstone")
	}
	fake.failDelete = false
	must(t, PurgeBackups(ctx, c, store, cfg, now))
	if _, found := getTombstone(c, db); found || fake.has(string(db.UID)+"/base/1") {
		t.Fatal("the retry did not finish")
	}
}

func TestSweeperPurgesOnlyWhileTheDatabaseKindsAreOn(t *testing.T) {
	c, fake, store, _ := purgeWorld(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, nil)
	now := mustTime("2026-10-19T12:00:00Z")
	fake.put(string(db.UID)+"/base/1", 10)
	must(t, EnsureTombstone(ctx, c, *db, project, 0, now))
	sweeper := &Sweeper{Client: c, Store: store, Now: func() time.Time { return now }}

	setSetting(t, c, "database.enabled", "false")
	setSetting(t, c, "database.backup.bucket", "backups")
	must(t, sweeper.Purge(ctx))
	if !fake.has(string(db.UID) + "/base/1") {
		t.Fatal("purged while disabled")
	}
	setSetting(t, c, "database.enabled", "true")
	must(t, sweeper.Purge(ctx))
	if fake.has(string(db.UID) + "/base/1") {
		t.Fatal("not purged while enabled")
	}
}

func TestPurgeWaitsWhileADatabaseStillRestoresFromThePrefix(t *testing.T) {
	c, fake, store, cfg := purgeWorld(t)
	ctx := context.Background()
	gone, project := storedDatabase(t, c, nil)
	now := mustTime("2026-10-19T12:00:00Z")
	fake.put(string(gone.UID)+"/base/1", 10)
	must(t, EnsureTombstone(ctx, c, *gone, project, 0, now))
	restored, _ := storedDatabase(t, c, func(db *v1alpha1.Database) { db.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: string(gone.UID)} })

	must(t, PurgeBackups(ctx, c, store, cfg, now))
	if !fake.has(string(gone.UID) + "/base/1") {
		t.Fatal("the prefix was purged while a restore reads it")
	}
	must(t, c.Get(ctx, client.ObjectKeyFromObject(restored), restored))
	restored.Annotations = map[string]string{annotationReadyOnce: "true"}
	must(t, c.Update(ctx, restored))
	must(t, PurgeBackups(ctx, c, store, cfg, now))
	if fake.has(string(gone.UID) + "/base/1") {
		t.Fatal("the prefix stayed after the restore finished")
	}
}
