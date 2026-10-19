package agent

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
)

const (
	encryptionDir       = "etc/bedrock/encryption"
	encryptionRetired   = "retired"
	encryptionFailure   = ".apply-failed"
	encryptionHandled   = ".grant-handled"
	encryptionProgress  = ".apply-in-progress"
	encryptionPrevious  = ".apply-previous"
	encryptionFileStamp = ".yaml"
	localAPIServer      = "https://127.0.0.1:6443"
)

func syncEncryptionStatus(deps Deps, current v1alpha1.Host) *v1alpha1.EncryptionFileStatus {
	if !v1alpha1.HostHasRole(current, v1alpha1.RoleControlPlane) {
		return nil
	}
	content, err := os.ReadFile(filepath.Join(deps.Root, encryption.File))
	if err != nil {
		return nil
	}
	return &v1alpha1.EncryptionFileStatus{
		Hash:           encryption.Hash(content),
		RestartPending: encryptionRestartKnown(deps.Root, current.Status.Encryption),
		FailedHash:     readEncryptionFailure(deps.Root),
		HandledGrant:   readEncryptionMarker(deps.Root, encryptionHandled),
	}
}

func encryptionRestartKnown(root string, previous *v1alpha1.EncryptionFileStatus) bool {
	start, ok := apiserverStartTime(root)
	if !ok {
		return previous != nil && previous.RestartPending
	}
	return encryptionRestartPending(root, start)
}

func encryptionRestartPending(root string, apiserverStart time.Time) bool {
	info, err := os.Stat(filepath.Join(root, encryption.File))
	return err == nil && info.ModTime().After(apiserverStart.Add(apiserverStartSlack))
}

func readEncryptionFailure(root string) string {
	return readEncryptionMarker(root, encryptionFailure)
}

func readEncryptionMarker(root, name string) string {
	raw, err := os.ReadFile(filepath.Join(root, encryptionDir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
