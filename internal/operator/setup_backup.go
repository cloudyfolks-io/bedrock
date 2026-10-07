package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	backuparea "github.com/cloudyfolks-io/bedrock/internal/backup"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupBackup(mgr ctrl.Manager, _ release.Bundle) error {
	return backuparea.SetupWithManager(mgr)
}
