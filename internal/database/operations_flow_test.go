package database

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

func upgradeWorld(t *testing.T, backups bool) *world {
	t.Helper()
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) { spec.Backup.Enabled = backups })
	w.bringUp()
	ctx := context.Background()
	var current v1alpha1.DatabaseVersion
	must(t, w.c.Get(ctx, client.ObjectKey{Name: "postgresql-17"}, &current))
	current.Spec.UpgradeTo = []string{"18"}
	must(t, w.c.Update(ctx, &current))
	w.addVersion(v1alpha1.DatabaseVersionSpec{Engine: "postgresql", Major: "18", Image: strings.Replace(worldPostgresImage, "17.9", "18.6", 1), Port: 5432, Enabled: true})
	return w
}

func (w *world) requestUpgrade() {
	w.t.Helper()
	w.db.Spec.Version = "18"
	must(w.t, w.c.Update(context.Background(), w.db))
}

func (w *world) preBackups() []*unstructured.Unstructured {
	w.t.Helper()
	all, err := ListBackups(context.Background(), w.c, *w.db)
	must(w.t, err)
	var pre []*unstructured.Unstructured
	for _, backup := range all {
		if strings.Contains(backup.GetName(), "-pre-") {
			pre = append(pre, backup)
		}
	}
	return pre
}

func (w *world) addPreBackup(name, phase string, stopped time.Time, annotations map[string]string) {
	w.t.Helper()
	ctx := context.Background()
	backup, err := PreUpgradeBackup(v1alpha1.DatabaseEnginePostgreSQL, engine.Spec{Name: string(w.db.UID), Namespace: PlatformNamespace}, name)
	must(w.t, err)
	backup.SetAnnotations(annotations)
	must(w.t, w.c.Create(ctx, backup))
	must(w.t, unstructured.SetNestedField(backup.Object, phase, "status", "phase"))
	if phase == "completed" {
		must(w.t, unstructured.SetNestedField(backup.Object, stopped.UTC().Format(time.RFC3339), "status", "stoppedAt"))
	}
	must(w.t, w.c.Status().Update(ctx, backup))
}

func TestAnOldPreUpgradeBackupDoesNotCountAndANewOneIsRequested(t *testing.T) {
	w := upgradeWorld(t, true)
	w.addPreBackup(string(w.db.UID)+"-pre-1", "completed", w.clock.Add(-72*time.Hour), map[string]string{annotationUpgradeTarget: "18", annotationUpgradeRequest: w.clock.Add(-73 * time.Hour).UTC().Format(time.RFC3339)})
	w.requestUpgrade()
	w.reconcile()
	w.reconcile()
	if w.phase() != "Upgrading" || w.reason() != ReasonBackupRequired || len(w.preBackups()) != 2 {
		t.Fatalf("phase %s reason %s backups %d", w.phase(), w.reason(), len(w.preBackups()))
	}
	image, _, _ := unstructured.NestedString(w.cluster().Object, "spec", "imageName")
	if strings.Contains(image, ":18.") {
		t.Fatal("the upgrade started on a 72 hour old backup")
	}
}

func TestAPreUpgradeBackupForAnotherTargetDoesNotCount(t *testing.T) {
	w := upgradeWorld(t, true)
	w.addPreBackup(string(w.db.UID)+"-pre-2", "completed", w.clock.Add(-time.Hour), map[string]string{annotationUpgradeTarget: "17", annotationUpgradeRequest: w.clock.Add(-2 * time.Hour).UTC().Format(time.RFC3339)})
	w.requestUpgrade()
	w.reconcile()
	w.reconcile()
	if w.reason() != ReasonBackupRequired || len(w.preBackups()) != 2 {
		t.Fatalf("reason %s backups %d", w.reason(), len(w.preBackups()))
	}
	var requested bool
	for _, backup := range w.preBackups() {
		requested = requested || backup.GetAnnotations()[annotationUpgradeTarget] == "18" && backup.GetAnnotations()[annotationUpgradeRequest] != ""
	}
	if !requested {
		t.Fatal("the new backup does not record its target and request time")
	}
}

func TestAnUpgradeWithBackupsDisabledIsRefusedAndMakesNoBackup(t *testing.T) {
	w := upgradeWorld(t, false)
	image, _, _ := unstructured.NestedString(w.cluster().Object, "spec", "imageName")
	w.requestUpgrade()
	for range 5 {
		w.reconcile()
	}
	if w.reason() != "BackupsDisabled" || len(w.preBackups()) != 0 {
		t.Fatalf("reason %s backups %d", w.reason(), len(w.preBackups()))
	}
	if after, _, _ := unstructured.NestedString(w.cluster().Object, "spec", "imageName"); after != image {
		t.Fatal("the old version stopped running")
	}
}

func TestAFailedPreUpgradeBackupWaitsAndNeverGrows(t *testing.T) {
	w := upgradeWorld(t, true)
	ctx := context.Background()
	w.requestUpgrade()
	w.reconcile()
	pre := w.preBackups()
	if len(pre) != 1 {
		t.Fatalf("%d pre-upgrade backups", len(pre))
	}
	must(t, unstructured.SetNestedField(pre[0].Object, "failed", "status", "phase"))
	must(t, w.c.Status().Update(ctx, pre[0]))
	for range 20 {
		w.reconcile()
	}
	if len(w.preBackups()) != 1 || w.reason() != "UpgradeBackupFailed" {
		t.Fatalf("backups %d reason %s", len(w.preBackups()), w.reason())
	}
	w.clock = w.clock.Add(2 * time.Hour)
	w.reconcile()
	w.reconcile()
	after := w.preBackups()
	if len(after) != 1 || after[0].GetName() == pre[0].GetName() {
		t.Fatalf("after the backoff: %d backups, first %s", len(after), after[0].GetName())
	}
}

func mysqlWorld(t *testing.T, class string) *world {
	t.Helper()
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) { spec.Engine, spec.Version = v1alpha1.DatabaseEngineMySQL, "8.4" })
	setAddon(t, w.c, "percona-ps", true)
	w.addVersion(v1alpha1.DatabaseVersionSpec{Engine: "mysql", Major: "8.4", Image: mysqlImage, Port: 3306, Enabled: true, Companions: map[string]string{"haproxy": haproxyPercona}})
	ha := &v1alpha1.DatabaseClass{ObjectMeta: metav1.ObjectMeta{Name: w.class + "-ha"}, Spec: v1alpha1.DatabaseClassSpec{CPU: resource.MustParse("500m"), Memory: resource.MustParse("1Gi"), MinStorageGi: 5, MaxStorageGi: 100, HighAvailability: true, Enabled: true}}
	create(t, w.c, ha)
	if class == "ha" {
		w.db.Spec.Class = ha.Name
		must(t, w.c.Update(context.Background(), w.db))
	}
	return w
}

func (w *world) mysqlSize() int64 {
	w.t.Helper()
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(primaryGVK(v1alpha1.DatabaseEngineMySQL))
	must(w.t, w.c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: string(w.db.UID)}, cluster))
	size, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "mysql", "size")
	return size
}

func (w *world) bringUpMySQL() {
	w.t.Helper()
	w.reconcile()
	w.setMySQLReady()
	w.setSecurityGroupReady()
	w.reconcile()
	w.setDeploymentReady(PlatformNamespace, GateName(w.db.UID), 1)
	w.setDeploymentReady(w.namespace, EndpointName(*w.db), 1)
	w.reconcile()
}

func (w *world) switchClass(name string) {
	w.t.Helper()
	w.db.Spec.Class = name
	must(w.t, w.c.Update(context.Background(), w.db))
	w.reconcile()
}

func TestAMySQLHighAvailabilityFlipIsRefusedBeforeAndAfterTheFirstReady(t *testing.T) {
	cases := []struct {
		name         string
		start, flip  string
		ready        bool
		deleteOldest bool
		size         int64
	}{
		{"1 to 3 before ready", "small", "ha", false, false, 1},
		{"1 to 3 after ready", "small", "ha", true, false, 1},
		{"3 to 1 before ready", "ha", "small", false, false, 3},
		{"3 to 1 after ready", "ha", "small", true, false, 3},
		{"1 to 3 with the old class deleted", "small", "ha", true, true, 1},
		{"3 to 1 with the old class deleted", "ha", "small", true, true, 3},
	}
	for _, c := range cases {
		w := mysqlWorld(t, c.start)
		if c.ready {
			w.bringUpMySQL()
		} else {
			w.reconcile()
		}
		old := w.db.Spec.Class
		target := w.class
		if c.flip == "ha" {
			target = w.class + "-ha"
		}
		if c.deleteOldest {
			must(t, w.c.Delete(context.Background(), &v1alpha1.DatabaseClass{ObjectMeta: metav1.ObjectMeta{Name: old}}))
		}
		w.switchClass(target)
		if w.reason() != ReasonClassChangeNotSupported || w.mysqlSize() != c.size {
			t.Errorf("%s: reason %s size %d", c.name, w.reason(), w.mysqlSize())
		}
	}
}

func TestAMySQLSchemaNamedAppOrASystemSchemaIsRefusedAndNeverDropped(t *testing.T) {
	c := startEnv(t)
	for _, name := range []string{"app", "mysql", "sys", "information_schema", "performance_schema"} {
		fake := &fakeEngine{}
		db := readyEngineDatabase(t, c, v1alpha1.DatabaseEngineMySQL)
		schema := &v1alpha1.DatabaseSchema{ObjectMeta: metav1.ObjectMeta{Name: "s" + nextSequence(), Namespace: db.Namespace}, Spec: v1alpha1.DatabaseSchemaSpec{DatabaseRef: db.Name, Name: name, DeletionPolicy: v1alpha1.DatabaseSchemaDelete}}
		create(t, c, schema)
		_, err := (&SchemaReconciler{Client: c, Engine: fake.factoryOf(v1alpha1.DatabaseEngineMySQL)}).Reconcile(context.Background(), requestOf(schema))
		must(t, err)
		must(t, c.Get(context.Background(), client.ObjectKeyFromObject(schema), schema))
		if reason, _ := readyReasonOf(schema.Status.Conditions); reason != ReasonInvalidSpec || len(fake.scripts) != 0 {
			t.Errorf("%s: reason %s scripts %v", name, reason, fake.scripts)
		}
	}
}
