package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

var (
	errImportBinding    = errors.New("the import material is bound to a different database")
	errMissingImportTLS = errors.New("the imported customer TLS Secret is missing")
	errInvalidImportTLS = errors.New("the imported customer TLS Secret is invalid")
)

func readImport(ctx context.Context, reader client.Reader, db v1alpha1.Database, name, source string) (*corev1.Secret, error) {
	if db.UID == "" {
		return nil, nil
	}
	var secret corev1.Secret
	bound := v1alpha1.BoundDatabaseImportName(name, string(db.UID))
	if err := reader.Get(ctx, client.ObjectKey{Namespace: v1alpha1.DatabaseImportNamespace, Name: bound}, &secret); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	if secret.Annotations[v1alpha1.DatabaseImportProject] != db.Namespace ||
		secret.Annotations[v1alpha1.DatabaseImportTarget] != db.Name ||
		secret.Annotations[v1alpha1.DatabaseImportTargetUID] != string(db.UID) ||
		secret.Annotations[v1alpha1.DatabaseImportSource] != "databases/"+source {
		return nil, errImportBinding
	}
	return &secret, nil
}

func importedTLS(ctx context.Context, reader client.Reader, db v1alpha1.Database) (*corev1.Secret, error) {
	secret, err := readImport(ctx, reader, db, v1alpha1.DatabaseImportTLSName(db.Name), db.Name)
	if err != nil {
		return nil, err
	}
	if secret == nil {
		if hasLegacyEndpoint(db) {
			return nil, errMissingImportTLS
		}
		return nil, nil
	}
	if db.Spec.Endpoint == nil {
		return nil, fmt.Errorf("%w: the database has no endpoint", errInvalidImportTLS)
	}
	if secret.Type != corev1.SecretTypeTLS {
		return nil, fmt.Errorf("%w: use a Secret of type kubernetes.io/tls", errInvalidImportTLS)
	}
	if err := v1alpha1.CheckDatabaseTLS(db.Spec.Endpoint.Address, secret.Data["tls.crt"], secret.Data["tls.key"], secret.Data["ca.crt"]); err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidImportTLS, err)
	}
	return secret, nil
}

func engineUsesImportedTLS(db v1alpha1.Database, primary *unstructured.Unstructured) bool {
	if primary == nil {
		return false
	}
	name := v1alpha1.BoundDatabaseImportName(v1alpha1.DatabaseImportTLSName(db.Name), string(db.UID))
	switch db.Spec.Engine {
	case v1alpha1.DatabaseEnginePostgreSQL:
		secret, _, _ := unstructured.NestedString(primary.Object, "spec", "certificates", "serverTLSSecret")
		return secret == name
	case v1alpha1.DatabaseEngineMySQL:
		secret, _, _ := unstructured.NestedString(primary.Object, "spec", "sslSecretName")
		return secret == name
	case v1alpha1.DatabaseEngineValkey:
		volumes, _, _ := unstructured.NestedSlice(primary.Object, "spec", "template", "spec", "volumes")
		for _, volume := range volumes {
			fields, _ := volume.(map[string]any)
			secret, _, _ := unstructured.NestedString(fields, "secret", "secretName")
			if secret == name {
				return true
			}
		}
	}
	return false
}

func importedRestore(ctx context.Context, reader client.Reader, db v1alpha1.Database) (*engine.RestoreBackup, error) {
	if db.Spec.Restore == nil {
		return nil, nil
	}
	source := db.Spec.Restore.SourceUID
	secret, err := readImport(ctx, reader, db, v1alpha1.DatabaseImportBackupName(source), source)
	if err != nil || secret == nil {
		return nil, err
	}
	bucket, prefix, server := string(secret.Data["bucket"]), string(secret.Data["prefix"]), string(secret.Data["serverName"])
	if bucket == "" || strings.ContainsAny(bucket, "/?#%@") || prefix != source || server != source ||
		len(secret.Data["ACCESS_KEY_ID"]) == 0 || len(secret.Data["ACCESS_SECRET_KEY"]) == 0 {
		return nil, fmt.Errorf("the imported backup location or credentials are incomplete")
	}
	destination := string(secret.Data["destination"])
	if db.Spec.Engine == v1alpha1.DatabaseEngineMySQL {
		base := "s3://" + bucket + "/" + prefix + "/"
		suffix := strings.TrimPrefix(destination, base)
		if suffix == destination || suffix == "" || strings.ContainsAny(suffix, "/?#%") {
			return nil, fmt.Errorf("the imported MySQL backup destination is outside its source prefix")
		}
	}
	return &engine.RestoreBackup{Bucket: bucket, Prefix: prefix, ServerName: server, SecretName: secret.Name, Destination: destination}, nil
}

func restoreCredentials(ctx context.Context, reader client.Reader, source *engine.RestoreBackup) (s3.Credentials, error) {
	if source == nil {
		return BackupCredentials(ctx, reader)
	}
	var secret corev1.Secret
	if err := reader.Get(ctx, client.ObjectKey{Namespace: v1alpha1.DatabaseImportNamespace, Name: source.SecretName}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return s3.Credentials{}, fmt.Errorf("the imported backup credentials disappeared")
		}
		return s3.Credentials{}, err
	}
	return BackupStore{}.Credentials(secret), nil
}
