package database

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
)

func importFixture(db v1alpha1.Database, name, source string, fields map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: v1alpha1.BoundDatabaseImportName(name, string(db.UID)), Namespace: v1alpha1.DatabaseImportNamespace,
		Annotations: map[string]string{
			v1alpha1.DatabaseImportSource: "databases/" + source, v1alpha1.DatabaseImportProject: db.Namespace, v1alpha1.DatabaseImportTarget: db.Name,
			v1alpha1.DatabaseImportTargetUID: string(db.UID),
		},
	}, Data: fields}
}

func TestImportedBackupKeepsTheSourceBucketAndCredentials(t *testing.T) {
	client, fake, store := backupWorld(t)
	ctx := context.Background()
	target := targetOf("acme-web", func(db *v1alpha1.Database) { db.UID = "12345678-1234-1234-1234-123456789012" })
	source := target.Spec.Restore.SourceUID
	secret := importFixture(target, v1alpha1.DatabaseImportBackupName(source), source, map[string][]byte{
		"bucket": []byte("db-" + source), "prefix": []byte(source), "serverName": []byte(source),
		"ACCESS_KEY_ID": []byte("source-key"), "ACCESS_SECRET_KEY": []byte("source-secret"),
	})
	must(t, client.Create(ctx, secret))
	fake.put(source+"/base/1", 10)
	check, err := LoadRestoreCheck(ctx, client, store, Config{BackupBucket: "regional"}, target, restoreNow)
	must(t, err)
	if check.PrefixObjects != 1 || check.Imported == nil || check.Imported.Bucket != "db-"+source || check.Imported.SecretName != secret.Name {
		t.Fatalf("restore did not use the imported backup location")
	}
	secret.Data["serverName"] = []byte("db-" + source)
	must(t, client.Update(ctx, secret))
	check, err = LoadRestoreCheck(ctx, client, store, Config{BackupBucket: "regional"}, target, restoreNow)
	must(t, err)
	if check.Imported == nil || check.Imported.Prefix != source || check.Imported.ServerName != "db-"+source {
		t.Fatal("the imported PostgreSQL archive lost its original server name")
	}
	mysql := target
	mysql.Spec.Engine = v1alpha1.DatabaseEngineMySQL
	secret.Data["destination"] = []byte("s3://db-" + source + "/" + source + "/full")
	must(t, client.Update(ctx, secret))
	if _, err := importedRestore(ctx, client, mysql); err == nil {
		t.Fatal("MySQL accepted a PostgreSQL backup server name")
	}
	secret.Data["serverName"] = []byte("db-" + string(target.UID))
	must(t, client.Update(ctx, secret))
	if _, err := importedRestore(ctx, client, target); err == nil {
		t.Fatal("PostgreSQL accepted a different source database ID")
	}
	secret.Data["serverName"] = []byte(source)
	must(t, client.Update(ctx, secret))
	foreign := target
	foreign.Namespace = "another-project"
	if _, err := importedRestore(ctx, client, foreign); err == nil {
		t.Fatal("another project can read an imported backup")
	}
}

func TestImportedAdminPasswordIsUsedWhenTheEngineStarts(t *testing.T) {
	client := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, client, nil)
	secret := importFixture(*db, v1alpha1.DatabaseImportAdminName(db.Name), db.Name, map[string][]byte{
		"password": []byte("source-password"),
	})
	must(t, client.Create(ctx, secret))
	must(t, EnsureEngineSecrets(ctx, client, *db, project))
	state, err := ReadAdmin(ctx, client, *db)
	must(t, err)
	if state.Password != "source-password" {
		t.Fatal("the engine did not keep the customer administrator password")
	}
}

func TestImportedCertificateMustMatchTheCustomerAddress(t *testing.T) {
	client, _, _ := backupWorld(t)
	ctx := context.Background()
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.TLS.Certificates[0].Certificate[0]})
	rawKey, err := x509.MarshalPKCS8PrivateKey(server.TLS.Certificates[0].PrivateKey)
	must(t, err)
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey})
	db := targetOf("acme-web", func(db *v1alpha1.Database) {
		db.UID = "12345678-1234-1234-1234-123456789012"
		db.Spec.Endpoint = &v1alpha1.DatabaseEndpointSpec{Address: "127.0.0.1", LoadBalancerRef: "source-load-balancer", Port: 6432}
	})
	if _, err := importedTLS(ctx, client, db); !errors.Is(err, errMissingImportTLS) {
		t.Fatal("an adopted endpoint was allowed without its customer certificate")
	}
	secret := importFixture(db, v1alpha1.DatabaseImportTLSName(db.Name), db.Name, map[string][]byte{
		"tls.crt": cert, "tls.key": key, "ca.crt": cert,
	})
	secret.Type = corev1.SecretTypeTLS
	must(t, client.Create(ctx, secret))
	loaded, err := importedTLS(ctx, client, db)
	must(t, err)
	if loaded == nil || loaded.Name != secret.Name {
		t.Fatal("the customer certificate was not selected")
	}
	recreated := db
	recreated.UID = "12345678-1234-1234-1234-123456789013"
	if _, err := importedTLS(ctx, client, recreated); err == nil {
		t.Fatal("a replacement database inherited the old customer certificate")
	}
	db.Spec.Endpoint.Address = "192.0.2.14"
	if _, err := importedTLS(ctx, client, db); err == nil {
		t.Fatal("the certificate was accepted for a different endpoint")
	}
}

type unavailableImportReader struct {
	client.Reader
	key client.ObjectKey
	err error
}

func (r unavailableImportReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if key == r.key {
		return r.err
	}
	return r.Reader.Get(ctx, key, object, options...)
}

func TestDatabaseReportsAnUnusableImportedCustomerTLSSecret(t *testing.T) {
	ctx := context.Background()
	const host = "10.100.0.20"
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) {
		spec.Endpoint = &v1alpha1.DatabaseEndpointSpec{Address: host}
	})
	public, private, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	certificate := &x509.Certificate{
		SerialNumber: big.NewInt(1), IPAddresses: []net.IP{net.ParseIP(host)},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, public, private)
	must(t, err)
	key, err := x509.MarshalPKCS8PrivateKey(private)
	must(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	secret := importFixture(*w.db, v1alpha1.DatabaseImportTLSName(w.db.Name), w.db.Name, map[string][]byte{
		"tls.crt": certPEM, "tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), "ca.crt": certPEM,
	})
	secret.Type = corev1.SecretTypeTLS
	must(t, w.c.Create(ctx, secret))
	w.bringUp()
	if w.phase() != v1alpha1.DatabasePhaseReady {
		t.Fatalf("the imported database did not reach Ready: %s", w.reason())
	}
	var copy corev1.Secret
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: w.namespace, Name: AdminCopyName(*w.db)}, &copy))
	if string(copy.Data["ca.crt"]) != string(certPEM) {
		t.Fatal("the credentials Secret did not keep the imported customer CA")
	}

	readFailure := errors.New("temporary customer TLS Secret read failure")
	w.r.Live = unavailableImportReader{Reader: w.c, key: client.ObjectKeyFromObject(secret), err: readFailure}
	_, err = w.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(w.db)})
	if !errors.Is(err, readFailure) {
		t.Fatalf("the API error must trigger a retry: %v", err)
	}
	must(t, w.c.Get(ctx, client.ObjectKeyFromObject(w.db), w.db))
	if w.phase() != v1alpha1.DatabasePhaseReady {
		t.Fatal("the API error changed the last observed state")
	}
	w.r.Live = w.c

	assertRefusal := func(message string) {
		t.Helper()
		before := w.cluster().GetResourceVersion()
		result := w.reconcile()
		ready := meta.FindStatusCondition(w.db.Status.Conditions, v1alpha1.ConditionReady)
		if w.phase() != v1alpha1.DatabasePhaseUnhealthy || ready == nil || ready.Status != metav1.ConditionFalse ||
			ready.Reason != ReasonApplyFailed || !strings.Contains(ready.Message, message) || result.RequeueAfter == 0 {
			t.Fatalf("certificate refusal: phase %s, ready %+v, result %+v", w.phase(), ready, result)
		}
		if !meta.IsStatusConditionTrue(w.db.Status.Conditions, ConditionEngineReady) || w.db.Status.Instances != 1 || w.cluster().GetResourceVersion() != before {
			t.Fatal("the certificate refusal changed the database engine or its observed health")
		}
	}
	secret.Data["tls.crt"] = []byte("invalid")
	must(t, w.c.Update(ctx, secret))
	assertRefusal("the imported customer TLS Secret is invalid")
	secret.Data["tls.crt"] = certPEM
	secret.Annotations[v1alpha1.DatabaseImportTargetUID] = "a-different-database"
	must(t, w.c.Update(ctx, secret))
	assertRefusal("bound to a different database")
	secret.Annotations[v1alpha1.DatabaseImportTargetUID] = string(w.db.UID)
	must(t, w.c.Update(ctx, secret))
	w.reconcile()
	if w.phase() != v1alpha1.DatabasePhaseReady {
		t.Fatalf("the repaired database did not return to Ready: %s", w.reason())
	}
	must(t, w.c.Delete(ctx, secret))
	assertRefusal("the imported customer TLS Secret is missing")
}

func TestImportedTLSReachesEveryEngine(t *testing.T) {
	db := targetOf("acme-web", func(db *v1alpha1.Database) { db.UID = "12345678-1234-1234-1234-123456789012" })
	base := engine.Spec{
		Name: "database", Namespace: "bedrock-dbaas", Image: "database:test",
		CPUMilli: 200, MemoryMi: 256, StorageGi: 10, Instances: 1,
		Images: map[string]string{"exporter": "exporter:test"}, TLSSecret: v1alpha1.BoundDatabaseImportName(v1alpha1.DatabaseImportTLSName(db.Name), string(db.UID)),
	}
	cases := []struct{ name, field string }{
		{v1alpha1.DatabaseEnginePostgreSQL, "serverTLSSecret"},
		{v1alpha1.DatabaseEngineMySQL, "sslSecretName"},
		{v1alpha1.DatabaseEngineValkey, "--tls-port"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			target := db
			target.Spec.Engine = test.name
			objects, err := renderFor(test.name, base)
			must(t, err)
			var found bool
			for _, object := range objects {
				raw, err := object.MarshalJSON()
				must(t, err)
				if strings.Contains(string(raw), base.TLSSecret) && strings.Contains(string(raw), test.field) && engineUsesImportedTLS(target, object) {
					found = true
				}
			}
			if !found {
				t.Fatal("the engine did not receive its customer certificate")
			}
		})
	}
}

func TestSelectedPostgresImportPreflight(t *testing.T) {
	c, fake, store := backupWorld(t)
	ctx := t.Context()
	target := targetOf("selected-test", func(db *v1alpha1.Database) {
		db.UID = "ac9a3716-988b-4703-b9ec-f25d8686315c"
		db.Annotations = map[string]string{v1alpha1.DatabaseImportMarker: "true"}
	})
	source := target.Spec.Restore.SourceUID
	id := "20261017T010000"
	name := v1alpha1.DatabaseImportBackupName(source)
	secret := importFixture(target, name, source, map[string][]byte{
		"bucket": []byte("db-" + source), "prefix": []byte(source), "serverName": []byte("db-" + source),
		"ACCESS_KEY_ID": []byte("key"), "ACCESS_SECRET_KEY": []byte("token"),
		v1alpha1.DatabaseImportBackupID: []byte(id), v1alpha1.DatabaseImportTargetImmediate: []byte("true"),
	})
	immutable := true
	secret.Immutable = &immutable
	must(t, c.Create(ctx, secret))
	path := source + "/db-" + source + "/base/" + id + "/backup.info"
	fake.put(source+"/db-"+source+"/base/20261018T010000/backup.info", 120)
	fake.put(path+".bak", 120)
	query := func() (RestoreCheck, error) {
		return LoadRestoreCheck(ctx, c, store, Config{BackupBucket: "global"}, target, restoreNow)
	}
	check, err := query()
	must(t, err)
	if check.PrefixObjects != 0 || check.Imported.Postgres.BackupID != id {
		t.Fatal("another base or metadata suffix satisfied exact-selection preflight")
	}
	fake.put(path, 0)
	check, err = query()
	must(t, err)
	if check.PrefixObjects != 0 {
		t.Fatal("empty backup.info satisfied preflight")
	}
	fake.put(path, 120)
	check, err = query()
	must(t, err)
	if check.PrefixObjects != 1 {
		t.Fatal("exact archived base was not observed")
	}
	must(t, c.Delete(ctx, secret))
	if _, err := query(); err == nil {
		t.Fatal("missing selected source fell back to shared credentials")
	}
}

func TestSelectedPostgresImportValidatesApproval(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC))
	for _, kind := range []string{"immediate", "time", "legacy", "missing id", "empty id", "invalid calendar id", "missing target", "false immediate", "two targets", "unexpected request time", "missing request time", "wrong time", "invalid time", "lost precision", "mutable", "mysql", "valkey", "destination"} {
		t.Run(kind, func(t *testing.T) {
			db := targetOf("target", func(*v1alpha1.Database) {})
			id, imm, timed := v1alpha1.DatabaseImportBackupID, v1alpha1.DatabaseImportTargetImmediate, v1alpha1.DatabaseImportTargetTime
			fields := map[string][]byte{id: []byte("20261017T010000"), imm: []byte("true")}
			s := importFixture(db, "backup", db.Spec.Restore.SourceUID, fields)
			immutable := true
			s.Immutable = &immutable
			switch kind {
			case "legacy":
				delete(fields, id)
				delete(fields, imm)
			case "missing id":
				delete(fields, id)
			case "empty id":
				fields[id] = nil
			case "invalid calendar id":
				fields[id] = []byte("20260230T010000")
			case "missing target":
				delete(fields, imm)
			case "false immediate":
				fields[imm] = []byte("false")
			case "two targets":
				fields[timed] = []byte(at.Format(time.RFC3339))
			case "unexpected request time":
				db.Spec.Restore.TargetTime = &at
			case "time", "missing request time", "wrong time", "invalid time", "lost precision":
				delete(fields, imm)
				fields[timed] = []byte("2026-10-18T04:32:03+03:30")
				db.Spec.Restore.TargetTime = &at
				switch kind {
				case "missing request time":
					db.Spec.Restore.TargetTime = nil
				case "wrong time":
					fields[timed] = []byte("2026-10-18T04:32:04+03:30")
				case "invalid time":
					fields[timed] = []byte("unbounded")
				case "lost precision":
					ns := metav1.NewTime(at.Add(time.Nanosecond))
					db.Spec.Restore.TargetTime = &ns
					fields[timed] = []byte(ns.Format(time.RFC3339Nano))
				}
			case "mutable":
				s.Immutable = nil
			case "mysql":
				db.Spec.Engine = v1alpha1.DatabaseEngineMySQL
			case "valkey":
				db.Spec.Engine = v1alpha1.DatabaseEngineValkey
			case "destination":
				fields["destination"] = []byte("s3://elsewhere")
			}
			got, err := importedPostgresPoint(db, s)
			switch kind {
			case "legacy":
				if err != nil || got != nil {
					t.Fatal("unselected import changed")
				}
			case "immediate":
				if err != nil || got == nil || got.BackupID != "20261017T010000" || got.TargetTime != nil {
					t.Fatalf("invalid immediate selection: %+v %v", got, err)
				}
			case "time":
				if err != nil || got == nil || got.TargetTime == nil || !got.TargetTime.Equal(at.Time) {
					t.Fatalf("invalid selected time: %+v %v", got, err)
				}
			default:
				if err == nil {
					t.Fatal("invalid import selection accepted")
				}
			}
		})
	}
}

func TestSelectedPostgresExistingBootstrap(t *testing.T) {
	c, _, store := backupWorld(t)
	ctx := t.Context()
	db := targetOf("existing-target", func(db *v1alpha1.Database) {
		db.UID = "446cb0ed-b11a-4914-befc-a441f607296a"
		db.Annotations = map[string]string{}
	})
	id := "20261017T010000"
	source := db.Spec.Restore.SourceUID
	secret := importFixture(db, v1alpha1.DatabaseImportBackupName(source), source, map[string][]byte{
		"bucket": []byte("imports"), "prefix": []byte(source), "serverName": []byte(source), "ACCESS_KEY_ID": []byte("key"), "ACCESS_SECRET_KEY": []byte("secret"),
		v1alpha1.DatabaseImportBackupID: []byte(id), v1alpha1.DatabaseImportTargetImmediate: []byte("true"),
	})
	immutable := true
	secret.Immutable = &immutable
	must(t, c.Create(ctx, secret))
	selected, err := importedRestore(ctx, c, db)
	must(t, err)
	rs := RestoreSpecOf(db)
	rs.Backup = selected
	s := engine.Spec{Name: Name(db.UID), Namespace: PlatformNamespace, CPUMilli: 100, MemoryMi: 256, StorageGi: 1, Instances: 1, Restore: rs, Backup: engine.BackupSpec{Endpoint: store.Endpoint}}
	objects, err := postgres.Render(s)
	must(t, err)
	var cluster, objectStore *unstructured.Unstructured
	for _, item := range objects {
		switch item.GetKind() {
		case "Cluster":
			cluster = Adopt(item, db, Project{})
		case "ObjectStore":
			objectStore = Adopt(item, db, Project{})
		}
	}
	must(t, c.Create(ctx, objectStore))
	r := &Reconciler{Client: c, Live: c, Store: store}
	desiredCluster := cluster.DeepCopy()
	must(t, applyObservedEngineObject(ctx, c, desiredCluster, nil))
	if err := applyObservedEngineObject(ctx, c, desiredCluster, nil); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("first admission replaced an existing Cluster: %v", err)
	}
	observed := cluster.DeepCopy()
	must(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), observed))
	admitted, _, err := unstructured.NestedSlice(observed.Object, "spec", "externalClusters")
	must(t, err)
	plugin, _, err := unstructured.NestedMap(admitted[0].(map[string]any), "plugin")
	must(t, err)
	if plugin["enabled"] != true || plugin["isWALArchiver"] != false {
		t.Fatal("the test API did not admit the pinned CNPG plugin defaults")
	}
	_, err = r.prepareRestore(ctx, &flow{db: &db, exists: true, cluster: observed})
	must(t, err)
	changed := observed.DeepCopy()
	changed.SetLabels(map[string]string{"test": "modified"})
	must(t, c.Update(ctx, changed))
	if err := applyObservedEngineObject(ctx, c, desiredCluster, observed); !apierrors.IsConflict(err) {
		t.Fatalf("the Cluster changed after inspection: %v", err)
	}
	foreign := objectStore.DeepCopy()
	foreign.SetAnnotations(nil)
	must(t, c.Update(ctx, foreign))
	if err := r.applySelectedPostgresStore(ctx, &flow{db: &db}, objectStore); !errors.Is(err, errChangedPostgresRestore) {
		t.Fatalf("initial admission adopted a foreign ObjectStore: %v", err)
	}
	objectStore.SetResourceVersion(foreign.GetResourceVersion())
	must(t, c.Update(ctx, objectStore))
	for _, ready := range []string{"", "true"} {
		db.Annotations[annotationReadyOnce] = ready
		for _, mode := range []string{"same", "admission defaults", "disabled plugin", "wrong WAL archiver", "extra plugin parameter", "extra bootstrap", "alternate backup", "alternate snapshot", "other id", "not selected", "source name", "foreign server"} {
			child := cluster.DeepCopy()
			switch mode {
			case "admission defaults", "disabled plugin", "wrong WAL archiver", "extra plugin parameter":
				sources, _, err := unstructured.NestedSlice(child.Object, "spec", "externalClusters")
				must(t, err)
				source := sources[0].(map[string]any)
				must(t, unstructured.SetNestedField(source, mode != "disabled plugin", "plugin", "enabled"))
				must(t, unstructured.SetNestedField(source, mode == "wrong WAL archiver", "plugin", "isWALArchiver"))
				if mode == "extra plugin parameter" {
					must(t, unstructured.SetNestedField(source, "extra", "plugin", "parameters", "extra"))
				}
				must(t, unstructured.SetNestedSlice(child.Object, sources, "spec", "externalClusters"))
			case "extra bootstrap":
				must(t, unstructured.SetNestedField(child.Object, map[string]any{}, "spec", "bootstrap", "initdb"))
			case "alternate backup":
				must(t, unstructured.SetNestedField(child.Object, map[string]any{"name": "another"}, "spec", "bootstrap", "recovery", "backup"))
			case "alternate snapshot":
				must(t, unstructured.SetNestedField(child.Object, map[string]any{}, "spec", "bootstrap", "recovery", "volumeSnapshots"))
			case "other id":
				must(t, unstructured.SetNestedField(child.Object, "20261018T010000", "spec", "bootstrap", "recovery", "recoveryTarget", "backupID"))
			case "not selected":
				unstructured.RemoveNestedField(child.Object, "spec", "bootstrap", "recovery", "recoveryTarget")
			case "source name":
				must(t, unstructured.SetNestedField(child.Object, "other", "spec", "bootstrap", "recovery", "source"))
			case "foreign server":
				unstructured.RemoveNestedField(child.Object, "spec", "externalClusters")
			}
			in := &flow{db: &db, exists: true, cluster: child}
			_, err := r.prepareRestore(ctx, in)
			if (err == nil) != (mode == "same" || mode == "admission defaults") {
				t.Fatalf("ready=%s mode=%s error=%v", ready, mode, err)
			}
		}
	}
	must(t, unstructured.SetNestedField(objectStore.Object, "s3://another/", "spec", "configuration", "destinationPath"))
	must(t, c.Update(ctx, objectStore))
	if _, err := r.prepareRestore(ctx, &flow{db: &db, cluster: cluster, exists: true}); !errors.Is(err, errChangedPostgresRestore) {
		t.Fatalf("source was repointed: %v", err)
	}
	must(t, c.Delete(ctx, secret))
	if _, err := r.prepareRestore(ctx, &flow{db: &db, cluster: cluster, exists: true}); !errors.Is(err, errChangedPostgresRestore) {
		t.Fatalf("existing selection was silently dropped: %v", err)
	}
}
