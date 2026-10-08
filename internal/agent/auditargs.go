package agent

import (
	"os"
	"path/filepath"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/audit"
)

const auditPolicyFlag = "--audit-policy-file"

func auditFilesPresent(root string) bool {
	if _, err := os.Stat(filepath.Join(root, audit.Dir, audit.PolicyFile)); err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(root, audit.LogDir))
	return err == nil && info.IsDir()
}

func ensureAuditArgs(root string, service k0sService) (bool, error) {
	if service.Unit != k0sControllerUnit || !auditFilesPresent(root) {
		return false, nil
	}
	return mergeAPIServerArgs(root, audit.Args())
}

func auditRestartPending(root string, apiserverStart time.Time) bool {
	info, err := os.Stat(filepath.Join(root, audit.Dir, audit.PolicyFile))
	return err == nil && info.ModTime().After(apiserverStart.Add(apiserverStartSlack))
}
