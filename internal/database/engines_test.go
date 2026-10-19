package database

import (
	"context"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func withEngine(engine string) func(*v1alpha1.DatabaseSpec) {
	return func(spec *v1alpha1.DatabaseSpec) {
		spec.Engine = engine
		spec.Backup.Enabled = engine != v1alpha1.DatabaseEngineValkey
	}
}

func hasFinalizer(t *testing.T, c client.Client, db *v1alpha1.Database) bool {
	t.Helper()
	var current v1alpha1.Database
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(db), &current))
	return controllerutil.ContainsFinalizer(&current, Finalizer)
}

func TestMySQLDatabaseNeedsThePerconaAddon(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "true")
	setAddon(t, c, "cnpg", true)
	db := newDatabase(t, c, projectNamespace(t, c), "orders", withEngine(v1alpha1.DatabaseEngineMySQL))
	reconcileOnce(t, c, db)
	if reason := readyReason(t, c, db); reason != ReasonEngineNotAvailable {
		t.Fatalf("only cnpg is on: reason %s", reason)
	}
	setAddon(t, c, "percona-ps", true)
	reconcileOnce(t, c, db)
	if !hasFinalizer(t, c, db) {
		t.Fatal("a MySQL database got no finalizer with the Percona addon on")
	}
}

func TestPostgreSQLDatabaseDoesNotRunOnThePerconaAddon(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "true")
	setAddon(t, c, "percona-ps", true)
	db := newDatabase(t, c, projectNamespace(t, c), "orders", nil)
	reconcileOnce(t, c, db)
	if reason := readyReason(t, c, db); reason != ReasonEngineNotAvailable {
		t.Fatalf("reason %s", reason)
	}
}

func TestValkeyDatabaseNeedsNoAddon(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "true")
	db := newDatabase(t, c, projectNamespace(t, c), "cache", withEngine(v1alpha1.DatabaseEngineValkey))
	reconcileOnce(t, c, db)
	if !hasFinalizer(t, c, db) {
		t.Fatal("a Valkey database got no finalizer without an addon")
	}
}

func TestValkeyDatabaseStillNeedsTheSetting(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "false")
	db := newDatabase(t, c, projectNamespace(t, c), "cache", withEngine(v1alpha1.DatabaseEngineValkey))
	reconcileOnce(t, c, db)
	if reason := readyReason(t, c, db); reason != ReasonDatabaseDisabled {
		t.Fatalf("reason %s", reason)
	}
}

func TestEngineAddonsAreWatched(t *testing.T) {
	for name, want := range map[string]bool{"cnpg": true, "percona-ps": true, "kata": false, "loki": false} {
		addon := &v1alpha1.Addon{}
		addon.Name = name
		if got := engineAddon(addon); got != want {
			t.Errorf("addon %s: watched %v, want %v", name, got, want)
		}
	}
}

func TestEngineGVKsCoverThePerconaAndValkeyObjects(t *testing.T) {
	kinds := EngineGVKs()
	for _, want := range []schema.GroupVersionKind{
		{Group: "ps.percona.com", Version: "v1", Kind: "PerconaServerMySQL"},
		{Group: "ps.percona.com", Version: "v1", Kind: "PerconaServerMySQLBackup"},
		{Group: "ps.percona.com", Version: "v1", Kind: "PerconaServerMySQLRestore"},
		{Group: "monitoring.coreos.com", Version: "v1", Kind: "PodMonitor"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Version: "v1", Kind: "PersistentVolumeClaim"},
	} {
		if !slices.Contains(kinds, want) {
			t.Errorf("EngineGVKs lacks %v", want)
		}
	}
}
