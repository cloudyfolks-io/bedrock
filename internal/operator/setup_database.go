package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/database"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupDatabase(mgr ctrl.Manager, bundle release.Bundle) error {
	return database.SetupWithManager(mgr, bundle.Images)
}
