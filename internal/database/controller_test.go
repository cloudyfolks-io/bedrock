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

func TestDeleteRemovesTheEngineObjectsLeavesTheTombstoneThenReleasesTheFinalizer(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	w.bringUp()
	if w.engineObjects() == 0 {
		t.Fatal("no engine object to delete")
	}
	other := engineSecret("other-"+nextSequence(), "someone-else", "other", w.namespace, "other-uid")
	create(t, w.c, other)

	must(t, w.c.Delete(ctx, w.db))
	first, err := w.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(w.db)})
	must(t, err)
	if first.RequeueAfter == 0 {
		t.Fatal("the first delete step must wait for the objects to go")
	}
	if w.engineObjects() != 0 {
		t.Fatal("an engine object of the database stayed")
	}
	tombstone, found := getTombstone(w.c, w.db)
	if !found || tombstone.Annotations[AnnotationDeleteAfter] == "" || tombstone.Data["namespace"] != w.namespace {
		t.Fatalf("tombstone %v", tombstone)
	}
	if !exists(w.c, other) {
		t.Fatal("the delete removed an object of another database")
	}
	second, err := w.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(w.db)})
	must(t, err)
	if second.RequeueAfter != 0 {
		t.Fatalf("requeue %v", second.RequeueAfter)
	}
	eventually(t, "the database to go away", func() bool { return !exists(w.c, w.db) })
}

func TestDeleteWithoutTheFinalizerNeedsNoWork(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "false")
	db := newDatabase(t, c, projectNamespace(t, c), "orders", nil)
	must(t, c.Delete(context.Background(), db))
	reconcileOnce(t, c, db)
	eventually(t, "the database to go away", func() bool { return !exists(c, db) })
}

func TestDeleteRemovesTheKeySecretsOfBothCertificatesAndLeavesForeignOnes(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	w.bringUp()
	uid := string(w.db.UID)
	labels := map[string]string{LabelInstance: uid, LabelManagedBy: ManagedBy}
	server := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: ServerSecretName(w.db.UID), Namespace: PlatformNamespace, Labels: labels}}
	client1 := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: EndpointName(*w.db), Namespace: w.namespace, Labels: labels}}
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orders-other", Namespace: w.namespace}}
	for _, secret := range []*corev1.Secret{server, client1, foreign} {
		create(t, w.c, secret)
	}
	must(t, w.c.Delete(ctx, w.db))
	for i := 0; i < 2; i++ {
		_, err := w.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(w.db)})
		must(t, err)
	}
	if exists(w.c, server) || exists(w.c, client1) {
		t.Fatal("a key Secret of the database stayed")
	}
	if !exists(w.c, foreign) {
		t.Fatal("the delete removed a Secret of the tenant")
	}
}

func TestDeleteDoesNotTakeASecretOfTheProxyNameThatTheDatabaseDoesNotOwn(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	w.bringUp()
	taken := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: EndpointName(*w.db), Namespace: w.namespace}}
	create(t, w.c, taken)
	must(t, DeleteProxySecret(ctx, w.c, *w.db))
	if !exists(w.c, taken) {
		t.Fatal("a Secret without the instance label was deleted")
	}
}
