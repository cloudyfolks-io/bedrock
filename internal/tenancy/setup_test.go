package tenancy

import (
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func TestSetupRegistersBothControllers(t *testing.T) {
	c, cfg := startEnv(t)
	startManager(t, cfg, SetupWithManager)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), projectFor("shop", "acme"))
	waitFor(t, func() bool { return getInto(t, c, "acme", &v1alpha1.Network{}) })
	waitFor(t, func() bool { return getInto(t, c, "acme-shop", &corev1.Namespace{}) })
}
