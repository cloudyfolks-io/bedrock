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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
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
