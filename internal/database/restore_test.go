package database

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var restoreNow = mustTime("2026-10-19T12:00:00Z")

func targetOf(namespace string, edit func(*v1alpha1.Database)) v1alpha1.Database {
	db := v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: "restored", Namespace: namespace},
		Spec:       newSpec(),
	}
	db.Spec.StorageGi = 20
	db.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "11111111-2222-3333-4444-555555555555"}
	if edit != nil {
		edit(&db)
	}
	return db
}

func sourceOf(namespace string, edit func(*v1alpha1.Database)) *v1alpha1.Database {
	db := &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: namespace},
		Spec:       newSpec(),
	}
	db.Spec.StorageGi = 20
	if edit != nil {
		edit(db)
	}
	return db
}

func TestCheckRestore(t *testing.T) {
	past := metav1.NewTime(restoreNow.Add(-time.Hour))
	future := metav1.NewTime(restoreNow.Add(time.Hour))
	cases := []struct {
		name   string
		check  RestoreCheck
		reason string
	}{
		{"no restore", RestoreCheck{Target: targetOf("a", func(db *v1alpha1.Database) { db.Spec.Restore = nil }), Now: restoreNow}, ""},
		{"a source that names no live object but has backups", RestoreCheck{Target: targetOf("a", nil), PrefixObjects: 3, Now: restoreNow}, ""},
		{"a live source of the same shape", RestoreCheck{Target: targetOf("a", nil), Source: sourceOf("a", nil), PrefixObjects: 3, Now: restoreNow}, ""},
		{"a live source with less storage", RestoreCheck{Target: targetOf("a", nil), Source: sourceOf("a", func(db *v1alpha1.Database) { db.Spec.StorageGi = 10 }), PrefixObjects: 1, Now: restoreNow}, ""},
		{"a past target time", RestoreCheck{Target: targetOf("a", func(db *v1alpha1.Database) { db.Spec.Restore.TargetTime = &past }), PrefixObjects: 1, Now: restoreNow}, ""},
		{"a future target time", RestoreCheck{Target: targetOf("a", func(db *v1alpha1.Database) { db.Spec.Restore.TargetTime = &future }), PrefixObjects: 1, Now: restoreNow}, ReasonRestoreTargetInFuture},
		{"an empty prefix", RestoreCheck{Target: targetOf("a", nil), Now: restoreNow}, ReasonRestoreSourceEmpty},
		{"a live source with an empty prefix", RestoreCheck{Target: targetOf("a", nil), Source: sourceOf("a", nil), Now: restoreNow}, ReasonRestoreSourceEmpty},
		{"another engine", RestoreCheck{Target: targetOf("a", nil), Source: sourceOf("a", func(db *v1alpha1.Database) { db.Spec.Engine = "mysql" }), PrefixObjects: 1, Now: restoreNow}, ReasonRestoreSourceMismatch},
		{"another version", RestoreCheck{Target: targetOf("a", nil), Source: sourceOf("a", func(db *v1alpha1.Database) { db.Spec.Version = "16" }), PrefixObjects: 1, Now: restoreNow}, ReasonRestoreSourceMismatch},
		{"less storage than the source", RestoreCheck{Target: targetOf("a", func(db *v1alpha1.Database) { db.Spec.StorageGi = 10 }), Source: sourceOf("a", nil), PrefixObjects: 1, Now: restoreNow}, ReasonRestoreSourceMismatch},
		{"a live source of another project", RestoreCheck{Target: targetOf("a", nil), Source: sourceOf("b", nil), PrefixObjects: 1, Now: restoreNow}, ReasonRestoreSourceMismatch},
		{"a deleted source of another project", RestoreCheck{Target: targetOf("a", nil), TombstoneNamespace: "b", PrefixObjects: 1, Now: restoreNow}, ReasonRestoreSourceMismatch},
		{"a deleted source of the same project", RestoreCheck{Target: targetOf("a", nil), TombstoneNamespace: "a", PrefixObjects: 1, Now: restoreNow}, ""},
	}
	for _, c := range cases {
		refusal := CheckRestore(c.check)
		switch {
		case c.reason == "" && refusal != nil:
			t.Errorf("%s: refused %+v", c.name, refusal)
		case c.reason != "" && (refusal == nil || refusal.Reason != c.reason || refusal.Message == ""):
			t.Errorf("%s: got %+v, want %s", c.name, refusal, c.reason)
		}
	}
}

func TestRestoreSpecOfCopiesTheSourceAndTheTargetTimeInUTC(t *testing.T) {
	if RestoreSpecOf(targetOf("a", func(db *v1alpha1.Database) { db.Spec.Restore = nil })) != nil {
		t.Fatal("a spec without restore gave a restore")
	}
	plain := RestoreSpecOf(targetOf("a", nil))
	if plain.SourceUID != "11111111-2222-3333-4444-555555555555" || plain.TargetTime != nil {
		t.Fatalf("plain %+v", plain)
	}
	zone := time.FixedZone("X", 3*3600+1800)
	at := metav1.NewTime(time.Date(2026, 10, 1, 15, 0, 0, 0, zone))
	timed := RestoreSpecOf(targetOf("a", func(db *v1alpha1.Database) { db.Spec.Restore.TargetTime = &at }))
	if timed.TargetTime == nil || timed.TargetTime.Location() != time.UTC || !timed.TargetTime.Equal(at.Time) {
		t.Fatalf("timed %+v", timed)
	}
}

func TestLoadRestoreCheckFindsALiveSourceTheTombstoneAndThePrefix(t *testing.T) {
	c, fake, store := backupWorld(t)
	ctx := context.Background()
	_, err := EnsureBackupStore(ctx, c, store, "backups")
	must(t, err)
	makeBackupUserReady(t, c, "AK", "SK")
	_, err = EnsureBackupStore(ctx, c, store, "backups")
	must(t, err)
	cfg, _ := ParseConfig(map[string]string{"database.backup.bucket": "backups"})

	source, project := storedDatabase(t, c, nil)
	target := targetOf(source.Namespace, func(db *v1alpha1.Database) { db.Spec.Restore.SourceUID = string(source.UID) })
	fake.put(string(source.UID)+"/base/1", 10)

	check, err := LoadRestoreCheck(ctx, c, store, cfg, target, restoreNow)
	must(t, err)
	if check.Source == nil || check.Source.UID != source.UID || check.PrefixObjects != 1 || check.TombstoneNamespace != "" {
		t.Fatalf("live source: %+v", check)
	}

	deleted := "22222222-3333-4444-5555-666666666666"
	gone := targetOf(source.Namespace, func(db *v1alpha1.Database) { db.Spec.Restore.SourceUID = deleted })
	tombstoned := v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: "gone", Namespace: "acme-web-old", UID: "22222222-3333-4444-5555-666666666666"}}
	must(t, EnsureTombstone(ctx, c, tombstoned, project, time.Hour, restoreNow))
	check, err = LoadRestoreCheck(ctx, c, store, cfg, gone, restoreNow)
	must(t, err)
	if check.Source != nil || check.TombstoneNamespace != "acme-web-old" || check.PrefixObjects != 0 {
		t.Fatalf("tombstoned source: %+v", check)
	}
	if refusal := CheckRestore(check); refusal == nil || refusal.Reason != ReasonRestoreSourceMismatch {
		t.Fatalf("refusal %+v", refusal)
	}

	fake.put("33333333-4444-5555-6666-777777777777/base/1", 10)
	imported := targetOf(source.Namespace, func(db *v1alpha1.Database) { db.Spec.Restore.SourceUID = "33333333-4444-5555-6666-777777777777" })
	check, err = LoadRestoreCheck(ctx, c, store, cfg, imported, restoreNow)
	must(t, err)
	if check.Source != nil || check.PrefixObjects != 1 || CheckRestore(check) != nil {
		t.Fatalf("a source that names no live object: %+v", check)
	}
}

func TestLoadRestoreCheckWithoutARestoreAsksNothing(t *testing.T) {
	c, fake, store := backupWorld(t)
	cfg, _ := ParseConfig(nil)
	check, err := LoadRestoreCheck(context.Background(), c, store, cfg, targetOf("a", func(db *v1alpha1.Database) { db.Spec.Restore = nil }), restoreNow)
	if err != nil || check.Source != nil || fake.listCount() != 0 {
		t.Fatalf("check %+v err %v lists %d", check, err, fake.listCount())
	}
}
