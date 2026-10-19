package registry

import "github.com/cloudyfolks-io/bedrock/internal/addons"

const (
	Namespace = addons.RegistryNamespace

	adminSecret               = "harbor-admin"
	coreKeysSecret            = "harbor-core-keys"
	jobserviceKeysSecret      = "harbor-jobservice-keys"
	registryKeysSecret        = "harbor-registry-keys"
	registryCredentialsSecret = "harbor-registry-credentials"
	databaseCredentialsSecret = "harbor-db-credentials"
	storeIdentityName         = "bedrock-registry"
	tokenCertificateName      = "harbor-token"
	databaseClusterName       = "harbor-db"
	databaseUser              = "harbor"
	databaseName              = "registry"
	databaseStorageSize       = "20Gi"
	backupObjectStoreName     = "harbor-db-backups"
	backupScheduleName        = "harbor-db-daily"
	backupPrefix              = "registry"
	backupRetention           = "30d"
	backupSchedule            = "0 0 2 * * *"
	barmanPlugin              = "barman-cloud.cloudnative-pg.io"
	registryUsername          = "harbor_registry_user"
	publicName                = "registry-public"
	publicSecretName          = "registry-public-tls"
	selfSignedIssuer          = "bedrock-selfsigned"
	caIssuer                  = "bedrock-ca"
	letsEncryptIssuer         = "letsencrypt"
	tlsModeKey                = "platform.tls-mode"
	tlsModeLetsEncrypt        = "LetsEncrypt"
	managedByLabel            = "app.kubernetes.io/managed-by"
	managedByValue            = "bedrock-registry"

	AnnotationAdopt                = "bedrock.cloudyfolks.io/adopt"
	AnnotationReservedNameApproved = "bedrock.cloudyfolks.io/reserved-name-approved"
	AnnotationResetSecret          = "bedrock.cloudyfolks.io/reset-secret"

	DatabaseImage = "ghcr.io/cloudnative-pg/postgresql:17.9-minimal-trixie@sha256:3c9f2df53e15f3fff0aee630beffe73936590333b7fd80f2239bf59b22a9852e"
)
