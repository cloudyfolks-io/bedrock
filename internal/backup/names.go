package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
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
	HoldAnnotation        = v1alpha1.AnnotationRestoreHold
	BackupVMLabel         = "bedrock.cloudyfolks.io/backup-vm"
	ExcludeFromBackup     = "velero.io/exclude-from-backup"
	VMSnapshotAnnotation  = "bedrock.cloudyfolks.io/virtual-machine"
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
