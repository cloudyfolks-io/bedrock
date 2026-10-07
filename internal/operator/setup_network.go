package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupNetwork(mgr ctrl.Manager, _ release.Bundle) error {
	return network.SetupWithManager(mgr)
}
