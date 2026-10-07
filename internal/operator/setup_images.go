package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/images"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupImages(mgr ctrl.Manager, _ release.Bundle) error {
	return images.SetupWithManager(mgr)
}
