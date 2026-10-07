package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/tenancy"
)

func setupTenancy(mgr ctrl.Manager, _ release.Bundle) error {
	return tenancy.SetupWithManager(mgr)
}
