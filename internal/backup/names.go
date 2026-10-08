package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const (
	VeleroNamespace       = "velero"
	LocationName          = "default"
	CredentialsSecretName = "bedrock-backup-credentials"
	CredentialsKey        = "cloud"
	RepositorySecretName  = "velero-repo-credentials"
	RepositoryKey         = "repository-password"
	SnapshotClassName     = "velero-block"
	RBDDriver             = "rook-ceph.rbd.csi.ceph.com"
	HoldAnnotation        = "bedrock.cloudyfolks.io/restore-hold"
	BackupVMLabel         = "bedrock.cloudyfolks.io/backup-vm"
	BackupFinalizer       = "bedrock.cloudyfolks.io/backup"
	RestoreFinalizer      = "bedrock.cloudyfolks.io/restore"
	MinScheduleInterval   = time.Hour
	OneShotTTL            = 720 * time.Hour
	ScheduleTTL           = 8760 * time.Hour
	DefaultKeep           = 7
	MaxKeep               = 365

	nameCut    = 24
	hashLength = 10
)

func VeleroName(prefix, namespace, name string) string {
	cut := name
	if len(cut) > nameCut {
		cut = cut[:nameCut]
	}
	cut = strings.TrimRight(cut, "-.")
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	return prefix + "-" + cut + "-" + hex.EncodeToString(sum[:])[:hashLength]
}
