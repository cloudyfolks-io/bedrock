package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupObjectStorage(mgr ctrl.Manager, _ release.Bundle) error {
	return objectstorage.SetupWithManager(mgr)
}
