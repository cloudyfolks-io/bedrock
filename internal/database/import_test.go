package database

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	if _, err := importedTLS(ctx, client, db); err == nil {
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

func TestImportedTLSReachesEveryEngine(t *testing.T) {
	base := engine.Spec{
		Name: "database", Namespace: "bedrock-dbaas", Image: "database:test",
		CPUMilli: 200, MemoryMi: 256, StorageGi: 10, Instances: 1,
		Images: map[string]string{"exporter": "exporter:test"}, TLSSecret: "import-tls-source",
	}
	cases := []struct{ name, field string }{
		{v1alpha1.DatabaseEnginePostgreSQL, "serverTLSSecret"},
		{v1alpha1.DatabaseEngineMySQL, "sslSecretName"},
		{v1alpha1.DatabaseEngineValkey, "--tls-port"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			objects, err := renderFor(test.name, base)
			must(t, err)
			var found bool
			for _, object := range objects {
				raw, err := object.MarshalJSON()
				must(t, err)
				if strings.Contains(string(raw), base.TLSSecret) && strings.Contains(string(raw), test.field) {
					found = true
				}
			}
			if !found {
				t.Fatal("the engine did not receive its customer certificate")
			}
		})
	}
}
