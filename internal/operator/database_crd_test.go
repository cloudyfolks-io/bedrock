package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func newDatabase(name, engine string) *v1alpha1.Database {
	return &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.DatabaseSpec{
			Engine: engine, Version: "17", Class: "small", StorageGi: 10,
			Backup: v1alpha1.DatabaseBackupSpec{Enabled: engine != "valkey"},
		},
	}
}

func reload(t *testing.T, c client.Client, obj *v1alpha1.Database) {
	t.Helper()
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	good := newDatabase("orders", "postgresql")
	good.Spec.AllowedCIDRs = []string{"10.0.0.0/24", "192.168.1.0/28"}
	createValid(t, c, good)
	reload(t, c, good)
	if good.Spec.SubnetRef != "default" || !good.Spec.Backup.Enabled {
		t.Fatalf("defaults not applied: %+v", good.Spec)
	}

	good.Status.Phase = v1alpha1.DatabasePhaseReady
	good.Status.Addresses = []v1alpha1.DatabaseAddress{{Subnet: "default", IP: "10.0.0.5", Port: 5432}}
	good.Status.Instances = 1
	good.Status.Backup = &v1alpha1.DatabaseBackupStatus{SizeBytes: 12, Items: []v1alpha1.DatabaseBackupItem{{ID: "b1", Phase: "completed"}}}
	good.Status.Stats = &v1alpha1.DatabaseStats{Connections: 3}
	if err := c.Status().Update(ctx, good); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	createInvalid(t, c, newDatabase("unknown", "oracle"), "spec.engine")
	bad := newDatabase("bad-phase", "postgresql")
	createValid(t, c, bad)
	bad.Status.Phase = "Sleeping"
	if err := c.Status().Update(ctx, bad); err == nil {
		t.Fatal("an unknown phase was accepted")
	}

	badCIDR := newDatabase("bad-cidr", "postgresql")
	badCIDR.Spec.AllowedCIDRs = []string{"10.0.0.0/33"}
	createInvalid(t, c, badCIDR, "allowedCIDRs entries must be IPv4 CIDRs")
	ipv6 := newDatabase("ipv6-cidr", "postgresql")
	ipv6.Spec.AllowedCIDRs = []string{"fd00::/64"}
	createInvalid(t, c, ipv6, "allowedCIDRs entries must be IPv4 CIDRs")

	zero := newDatabase("zero-storage", "postgresql")
	zero.Spec.StorageGi = 0
	createInvalid(t, c, zero, "spec.storageGi")
	long := newDatabase("a1234567890123456789012345678901234567890123456789012", "postgresql")
	createInvalid(t, c, long, "at most 52 characters")
}

func TestDatabaseValkeyHasNoBackups(t *testing.T) {
	c, _ := StartTestEnv(t)

	createValid(t, c, newDatabase("cache", "valkey"))
	withBackup := newDatabase("cache-backup", "valkey")
	withBackup.Spec.Backup.Enabled = true
	createInvalid(t, c, withBackup, "a Valkey database has no backups")
	withRestore := newDatabase("cache-restore", "valkey")
	withRestore.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "abc"}
	createInvalid(t, c, withRestore, "a Valkey database has no restore")
	mysqlRestore := newDatabase("sql-restore", "mysql")
	mysqlRestore.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "abc"}
	createValid(t, c, mysqlRestore)
	defaulted := newDatabase("cache-default", "valkey")
	defaulted.Spec.Backup = v1alpha1.DatabaseBackupSpec{}
	createValid(t, c, defaulted)
	reload(t, c, defaulted)
	if defaulted.Spec.Backup.Enabled {
		t.Fatal("backup.enabled=false was turned into true")
	}
}

func TestDatabaseImmutableFields(t *testing.T) {
	c, _ := StartTestEnv(t)

	db := newDatabase("orders", "postgresql")
	createValid(t, c, db)

	engine := db.DeepCopy()
	engine.Spec.Engine = "mysql"
	updateInvalid(t, c, engine, "engine is immutable")

	subnet := db.DeepCopy()
	subnet.Spec.SubnetRef = "other"
	updateInvalid(t, c, subnet, "subnetRef is immutable")

	restore := db.DeepCopy()
	restore.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "abc"}
	updateInvalid(t, c, restore, "restore is immutable")

	allowed := db.DeepCopy()
	allowed.Spec.AllowedCIDRs = []string{"10.1.0.0/16"}
	allowed.Spec.Suspended = true
	allowed.Spec.Class = "large"
	if err := c.Update(context.Background(), allowed); err != nil {
		t.Fatalf("other fields stay mutable: %v", err)
	}
}

func TestDatabaseVersionMayChangeAndTheControllerGatesTheUpgrade(t *testing.T) {
	c, _ := StartTestEnv(t)

	db := newDatabase("orders", "postgresql")
	createValid(t, c, db)
	upgraded := db.DeepCopy()
	upgraded.Spec.Version = "18"
	if err := c.Update(context.Background(), upgraded); err != nil {
		t.Fatalf("the API accepts a version change and the controller gates it: %v", err)
	}
}

func TestDatabaseRestoreIsImmutableOnceSet(t *testing.T) {
	c, _ := StartTestEnv(t)

	db := newDatabase("restored", "postgresql")
	db.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "11111111-2222-3333-4444-555555555555"}
	createValid(t, c, db)

	changed := db.DeepCopy()
	changed.Spec.Restore.SourceUID = "other"
	updateInvalid(t, c, changed, "restore is immutable")

	removed := db.DeepCopy()
	removed.Spec.Restore = nil
	updateInvalid(t, c, removed, "restore is immutable")
}

func TestDatabaseStorageDoesNotShrink(t *testing.T) {
	c, _ := StartTestEnv(t)

	db := newDatabase("orders", "postgresql")
	createValid(t, c, db)

	smaller := db.DeepCopy()
	smaller.Spec.StorageGi = 5
	updateInvalid(t, c, smaller, "storageGi cannot shrink")

	larger := db.DeepCopy()
	larger.Spec.StorageGi = 20
	if err := c.Update(context.Background(), larger); err != nil {
		t.Fatalf("growth must pass: %v", err)
	}
}
