package database

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func newSpec() v1alpha1.DatabaseSpec {
	return v1alpha1.DatabaseSpec{
		Engine: v1alpha1.DatabaseEnginePostgreSQL, Version: "17", Class: "small", StorageGi: 10,
		Backup: v1alpha1.DatabaseBackupSpec{Enabled: true},
	}
}

func mustQuantity(value string) resource.Quantity { return resource.MustParse(value) }

func newDatabase(t testing.TB, c client.Client, namespace, name string, edit func(*v1alpha1.DatabaseSpec)) *v1alpha1.Database {
	t.Helper()
	db := &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: newSpec()}
	if edit != nil {
		edit(&db.Spec)
	}
	create(t, c, db)
	return db
}

func reconcileOnce(t testing.TB, c client.Client, db *v1alpha1.Database) ctrl.Result {
	t.Helper()
	result, err := (&Reconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(db)})
	must(t, err)
	return result
}

func readyReason(t testing.TB, c client.Client, db *v1alpha1.Database) string {
	t.Helper()
	var current v1alpha1.Database
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(db), &current))
	ready := meta.FindStatusCondition(current.Status.Conditions, v1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Message == "" {
		t.Fatalf("ready condition %+v", ready)
	}
	return ready.Reason
}

func TestDatabaseShowsDatabaseDisabledWhileTheSettingIsOff(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "false")
	setAddon(t, c, "cnpg", true)
	db := newDatabase(t, c, projectNamespace(t, c), "orders", nil)
	reconcileOnce(t, c, db)
	if reason := readyReason(t, c, db); reason != ReasonDatabaseDisabled {
		t.Fatalf("reason %s", reason)
	}
	var current v1alpha1.Database
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(db), &current))
	if controllerutil.ContainsFinalizer(&current, Finalizer) {
		t.Fatal("a disabled database got a finalizer")
	}
}

func TestDatabaseShowsEngineNotAvailableWithoutTheAddon(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "true")
	namespace := projectNamespace(t, c)
	db := newDatabase(t, c, namespace, "orders", nil)
	reconcileOnce(t, c, db)
	if reason := readyReason(t, c, db); reason != ReasonEngineNotAvailable {
		t.Fatalf("no addon: reason %s", reason)
	}
	setAddon(t, c, "cnpg", false)
	reconcileOnce(t, c, db)
	if reason := readyReason(t, c, db); reason != ReasonEngineNotAvailable {
		t.Fatalf("addon off: reason %s", reason)
	}
}

func TestDatabaseOfAnotherEngineShowsEngineNotAvailable(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "true")
	setAddon(t, c, "cnpg", true)
	db := newDatabase(t, c, projectNamespace(t, c), "cache", func(spec *v1alpha1.DatabaseSpec) {
		spec.Engine = v1alpha1.DatabaseEngineMySQL
	})
	reconcileOnce(t, c, db)
	if reason := readyReason(t, c, db); reason != ReasonEngineNotAvailable {
		t.Fatalf("reason %s", reason)
	}
}

func TestDatabaseOutsideAProjectShowsNotInProject(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "true")
	setAddon(t, c, "cnpg", true)
	namespace := "plain-" + nextSequence()
	create(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	db := newDatabase(t, c, namespace, "orders", nil)
	reconcileOnce(t, c, db)
	if reason := readyReason(t, c, db); reason != ReasonNotInProject {
		t.Fatalf("reason %s", reason)
	}
}

func TestDatabaseGetsTheFinalizerWhenTheEngineIsAvailable(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "true")
	setAddon(t, c, "cnpg", true)
	db := newDatabase(t, c, projectNamespace(t, c), "orders", nil)
	reconcileOnce(t, c, db)
	var current v1alpha1.Database
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(db), &current))
	if !controllerutil.ContainsFinalizer(&current, Finalizer) {
		t.Fatal("no finalizer")
	}
}

func TestDeleteRemovesTheEngineObjectsThenReleasesTheFinalizer(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	setSetting(t, c, "database.enabled", "true")
	setAddon(t, c, "cnpg", true)
	cfg, err := LoadConfig(ctx, c)
	must(t, err)
	must(t, EnsurePlatform(ctx, c, cfg))
	db := newDatabase(t, c, projectNamespace(t, c), "orders", nil)
	reconcileOnce(t, c, db)
	must(t, c.Get(ctx, client.ObjectKeyFromObject(db), db))

	mine := engineSecret("mine-"+nextSequence(), string(db.UID), db.Name, db.Namespace, string(db.UID))
	other := engineSecret("other-"+nextSequence(), "someone-else", "other", db.Namespace, "other-uid")
	create(t, c, mine)
	create(t, c, other)

	must(t, c.Delete(ctx, db))
	reconcileOnce(t, c, db)
	if exists(c, mine) {
		t.Fatal("an engine object of the database stayed")
	}
	if !exists(c, other) {
		t.Fatal("the delete removed an object of another database")
	}
	result := reconcileOnce(t, c, db)
	if result.RequeueAfter != 0 {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
	eventually(t, "the database to go away", func() bool { return !exists(c, db) })
}

func TestDeleteWithoutTheFinalizerNeedsNoWork(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "false")
	db := newDatabase(t, c, projectNamespace(t, c), "orders", nil)
	must(t, c.Delete(context.Background(), db))
	reconcileOnce(t, c, db)
	eventually(t, "the database to go away", func() bool { return !exists(c, db) })
}
