package database

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
)

const (
	valkeyImageOld = "docker.io/valkey/valkey:9.0.6@sha256:63346cb24a61221e76bdf41acce99b3968a9fa83d8122144deab45394b27b4f2"
	valkeyImageNew = "docker.io/valkey/valkey:9.0.7@sha256:73346cb24a61221e76bdf41acce99b3968a9fa83d8122144deab45394b27b4f2"
	exporterImage  = "docker.io/oliver006/redis_exporter:v1.94.0@sha256:0842ef7110e62b40bbde2d5c11f38c6fbd99f7100c75e3866637c1b335a1eff7"
	mysqlImage     = "docker.io/percona/percona-server:8.4.10-10.1@sha256:01cf1439241e11c5d49312db1b18ee01a5947869c1a1dec18417d88ffaeb5717"
	haproxyPercona = "docker.io/percona/haproxy:2.8.18-1@sha256:726b65930d86f7342eb6bce97a38b9af934c935345e4524afda6b65c2b3213b3"
)

func (w *world) addVersion(spec v1alpha1.DatabaseVersionSpec) {
	w.t.Helper()
	version := &v1alpha1.DatabaseVersion{ObjectMeta: metav1.ObjectMeta{Name: spec.Engine + "-" + spec.Major}, Spec: spec}
	if err := w.c.Create(context.Background(), version); err != nil && !apierrors.IsAlreadyExists(err) {
		w.t.Fatal(err)
	}
}

func valkeyWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) {
		spec.Engine, spec.Version, spec.Backup.Enabled = v1alpha1.DatabaseEngineValkey, "9.0", false
	})
	w.addVersion(v1alpha1.DatabaseVersionSpec{Engine: "valkey", Major: "9.0", Image: valkeyImageOld, Port: 6379, Enabled: true, Companions: map[string]string{"exporter": exporterImage}})
	return w
}

func (w *world) valkeyDeployment() appsv1.Deployment {
	w.t.Helper()
	var deployment appsv1.Deployment
	must(w.t, w.c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: string(w.db.UID)}, &deployment))
	return deployment
}

func (w *world) bringUpValkey() {
	w.t.Helper()
	w.reconcile()
	w.setDeploymentReady(PlatformNamespace, string(w.db.UID), 1)
	w.setSecurityGroupReady()
	w.reconcile()
	w.setDeploymentReady(PlatformNamespace, GateName(w.db.UID), 1)
	w.setDeploymentReady(w.namespace, EndpointName(*w.db), 1)
	w.reconcile()
}

func TestAValkeyDatabaseComesUpAndRecordsWhatItApplied(t *testing.T) {
	w := valkeyWorld(t)
	w.bringUpValkey()
	if w.phase() != "Ready" {
		t.Fatalf("phase %s reason %s", w.phase(), w.reason())
	}
	if got := w.valkeyDeployment().Spec.Template.Spec.Containers[1].Image; got != exporterImage {
		t.Errorf("exporter image %s", got)
	}
	if w.db.Status.Image != valkeyImageOld || w.db.Status.Applied == nil || w.db.Status.Applied.Version != "9.0" || w.db.Status.Instances != 1 {
		t.Fatalf("status image %q applied %+v instances %d", w.db.Status.Image, w.db.Status.Applied, w.db.Status.Instances)
	}
	if w.engine.count("INFO") == 0 {
		t.Error("the stats were not sampled")
	}
}

func TestSuspendScalesValkeyTheGateAndTheProxyToZero(t *testing.T) {
	w := valkeyWorld(t)
	w.bringUpValkey()
	ctx := context.Background()
	w.db.Spec.Suspended = true
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	var gate, proxy appsv1.Deployment
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: GateName(w.db.UID)}, &gate))
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: w.namespace, Name: EndpointName(*w.db)}, &proxy))
	if *w.valkeyDeployment().Spec.Replicas != 0 || *gate.Spec.Replicas != 0 || *proxy.Spec.Replicas != 0 || w.phase() != "Suspended" {
		t.Fatalf("replicas %d %d %d phase %s", *w.valkeyDeployment().Spec.Replicas, *gate.Spec.Replicas, *proxy.Spec.Replicas, w.phase())
	}
	w.db.Spec.Suspended = false
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	if *w.valkeyDeployment().Spec.Replicas != 1 {
		t.Fatal("resume did not scale the engine back")
	}
}

func TestACatalogChangeDoesNotRollAValkeyDatabaseButTheRefreshDoes(t *testing.T) {
	w := valkeyWorld(t)
	w.bringUpValkey()
	ctx := context.Background()
	var version v1alpha1.DatabaseVersion
	must(t, w.c.Get(ctx, client.ObjectKey{Name: "valkey-9.0"}, &version))
	version.Spec.Image = valkeyImageNew
	must(t, w.c.Update(ctx, &version))
	w.reconcile()
	if got := w.valkeyDeployment().Spec.Template.Spec.Containers[0].Image; got != valkeyImageOld {
		t.Fatalf("a catalog change rolled the database to %s", got)
	}
	w.db.Annotations = map[string]string{v1alpha1.AnnotationRestart: "t1", v1alpha1.AnnotationRefreshImage: "true"}
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	deployment := w.valkeyDeployment()
	if deployment.Spec.Template.Spec.Containers[0].Image != valkeyImageNew || deployment.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] != "t1" {
		t.Fatalf("refresh: image %s annotations %v", deployment.Spec.Template.Spec.Containers[0].Image, deployment.Spec.Template.Annotations)
	}
	w.reconcile()
	if w.db.Status.Image != valkeyImageNew || w.db.Status.Applied.RestartToken != "t1" {
		t.Fatalf("status image %s applied %+v", w.db.Status.Image, w.db.Status.Applied)
	}
	w.reconcile()
	if got := w.valkeyDeployment().Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; got != "t1" {
		t.Fatalf("a second reconcile restarted again: %s", got)
	}
}

func TestAnUpgradeWaitsForOneFreshBackup(t *testing.T) {
	w := newWorld(t, nil)
	w.bringUp()
	ctx := context.Background()
	var current v1alpha1.DatabaseVersion
	must(t, w.c.Get(ctx, client.ObjectKey{Name: "postgresql-17"}, &current))
	current.Spec.UpgradeTo = []string{"18"}
	must(t, w.c.Update(ctx, &current))
	w.addVersion(v1alpha1.DatabaseVersionSpec{Engine: "postgresql", Major: "18", Image: strings.Replace(worldPostgresImage, "17.9", "18.6", 1), Port: 5432, Enabled: true})
	w.db.Spec.Version = "18"
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	w.reconcile()
	if w.phase() != "Upgrading" || w.reason() != ReasonBackupRequired {
		t.Fatalf("phase %s reason %s", w.phase(), w.reason())
	}
	pre := 0
	backups, err := ListBackups(ctx, w.c, *w.db)
	must(t, err)
	for _, backup := range backups {
		if strings.Contains(backup.GetName(), "-pre-") {
			pre++
			must(t, unstructured.SetNestedField(backup.Object, "completed", "status", "phase"))
			must(t, unstructured.SetNestedField(backup.Object, w.clock.UTC().Format(time.RFC3339), "status", "stoppedAt"))
			must(t, w.c.Status().Update(ctx, backup))
		}
	}
	if pre != 1 {
		t.Fatalf("%d pre-upgrade backups", pre)
	}
	w.reconcile()
	image, _, _ := unstructured.NestedString(w.cluster().Object, "spec", "imageName")
	if !strings.Contains(image, ":18.6") || w.phase() != "Upgrading" || w.db.Status.Image != image {
		t.Fatalf("image %s phase %s status image %s", image, w.phase(), w.db.Status.Image)
	}
}

func (w *world) setMySQLReady() {
	w.t.Helper()
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(mysql.ClusterGVK)
	must(w.t, w.c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: string(w.db.UID)}, cluster))
	must(w.t, unstructured.SetNestedField(cluster.Object, "ready", "status", "state"))
	must(w.t, unstructured.SetNestedField(cluster.Object, int64(1), "status", "mysql", "ready"))
	must(w.t, w.c.Status().Update(context.Background(), cluster))
}

func TestAMySQLDatabaseComesUpThroughThePerconaCluster(t *testing.T) {
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) {
		spec.Engine, spec.Version = v1alpha1.DatabaseEngineMySQL, "8.4"
	})
	setAddon(t, w.c, "percona-ps", true)
	w.addVersion(v1alpha1.DatabaseVersionSpec{Engine: "mysql", Major: "8.4", Image: mysqlImage, Port: 3306, Enabled: true, Companions: map[string]string{"haproxy": haproxyPercona}})
	w.reconcile()
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(mysql.ClusterGVK)
	must(t, w.c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: string(w.db.UID)}, cluster))
	if image, _, _ := unstructured.NestedString(cluster.Object, "spec", "mysql", "image"); image != mysqlImage {
		t.Fatalf("mysql image %q", image)
	}
	w.setMySQLReady()
	w.setSecurityGroupReady()
	w.reconcile()
	w.setDeploymentReady(PlatformNamespace, GateName(w.db.UID), 1)
	w.setDeploymentReady(w.namespace, EndpointName(*w.db), 1)
	w.reconcile()
	if w.phase() != "Ready" || w.db.Status.Instances != 1 {
		t.Fatalf("phase %s reason %s instances %d", w.phase(), w.reason(), w.db.Status.Instances)
	}
	if w.engine.count("CREATE USER IF NOT EXISTS 'dbadmin'") == 0 {
		t.Error("the MySQL bootstrap did not run")
	}
	secret := getSecret(t, w.c, w.namespace, AdminCopyName(*w.db))
	if !strings.HasPrefix(string(secret.Data["uri"]), "mysql://dbadmin:") || string(secret.Data["database"]) != "app" {
		t.Fatalf("admin copy %v", secret.Data)
	}
}

func TestValkeyAndMySQLUsersGoThroughTheirOwnEngineScripts(t *testing.T) {
	c := startEnv(t)
	cases := []struct {
		kind, fragment string
		edit           func(*v1alpha1.DatabaseUser)
	}{
		{v1alpha1.DatabaseEngineMySQL, "CREATE USER 'alice'", nil},
		{v1alpha1.DatabaseEngineValkey, "~orders:*", func(user *v1alpha1.DatabaseUser) { user.Spec.KeyPrefix = "orders:" }},
	}
	for _, tc := range cases {
		engine := &fakeEngine{}
		db := readyEngineDatabase(t, c, tc.kind)
		user := newUser(t, c, db, "alice", tc.edit)
		reconcileUserOf(t, c, engine, tc.kind, user)
		reloadUser(t, c, user)
		if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady || engine.occurrences(tc.fragment) == 0 {
			t.Fatalf("%s: reason %s scripts %v", tc.kind, reason, engine.scripts)
		}
		secret := getSecret(t, c, db.Namespace, "alice-db")
		if scheme := map[string]string{v1alpha1.DatabaseEngineMySQL: "mysql://", v1alpha1.DatabaseEngineValkey: "redis://"}[tc.kind]; !strings.HasPrefix(string(secret.Data["uri"]), scheme) {
			t.Fatalf("%s: uri %s", tc.kind, secret.Data["uri"])
		}
	}
}

func TestValkeyUsersAndSchemasAreRefusedWhenInvalid(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyEngineDatabase(t, c, v1alpha1.DatabaseEngineValkey)
	user := newUser(t, c, db, "bob", func(user *v1alpha1.DatabaseUser) {
		user.Spec.Grants = []v1alpha1.DatabaseGrant{{Database: "other", Level: "readonly"}}
	})
	reconcileUserOf(t, c, engine, v1alpha1.DatabaseEngineValkey, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonInvalidSpec && reason != ReasonGrantDatabaseNotFound {
		t.Fatalf("reason %s", reason)
	}
	reserved := newUser(t, c, db, "svc", func(user *v1alpha1.DatabaseUser) { user.Spec.Username = "dbadmin" })
	reconcileUserOf(t, c, engine, v1alpha1.DatabaseEngineValkey, reserved)
	reloadUser(t, c, reserved)
	if reason, _ := readyReasonOf(reserved.Status.Conditions); reason != ReasonInvalidSpec || len(engine.scripts) != 0 {
		t.Fatalf("reserved name: reason %s scripts %v", reason, engine.scripts)
	}
	schema := &v1alpha1.DatabaseSchema{ObjectMeta: metav1.ObjectMeta{Name: "reports", Namespace: db.Namespace}, Spec: v1alpha1.DatabaseSchemaSpec{DatabaseRef: db.Name}}
	create(t, c, schema)
	_, err := (&SchemaReconciler{Client: c, Engine: engine.factoryOf(v1alpha1.DatabaseEngineValkey)}).Reconcile(context.Background(), requestOf(schema))
	must(t, err)
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(schema), schema))
	if reason, _ := readyReasonOf(schema.Status.Conditions); reason != ReasonUnsupportedForEngine {
		t.Fatalf("schema reason %s", reason)
	}
}

func (f *fakeEngine) factoryOf(kind string) EngineFactory {
	return func(context.Context, v1alpha1.Database) (Engine, error) {
		return Engine{Kind: kind, Run: f.run, Timeout: 2 * time.Minute}, nil
	}
}

func readyEngineDatabase(t *testing.T, c client.Client, kind string) *v1alpha1.Database {
	t.Helper()
	namespace := projectNamespace(t, c)
	spec := newSpec()
	spec.Engine = kind
	if kind == v1alpha1.DatabaseEngineValkey {
		spec.Backup.Enabled = false
	}
	db := &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: namespace}, Spec: spec}
	create(t, c, db)
	markEngineReady(t, c, db, true)
	return db
}

func reconcileUserOf(t *testing.T, c client.Client, engine *fakeEngine, kind string, user *v1alpha1.DatabaseUser) {
	t.Helper()
	_, err := (&UserReconciler{Client: c, Engine: engine.factoryOf(kind)}).Reconcile(context.Background(), requestOf(user))
	must(t, err)
}

func requestOf(object client.Object) ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKeyFromObject(object)}
}
