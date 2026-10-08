package tenancy

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func startEnv(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	return startEnvWithPaths(t, testenv.CRDPaths())
}

func startEnvWithPaths(t *testing.T, paths []string) (client.Client, *rest.Config) {
	t.Helper()
	env := &envtest.Environment{CRDDirectoryPaths: paths, ErrorIfCRDPathMissing: true}
	env.ControlPlane.GetAPIServer().Configure().Append("disable-admission-plugins", "TaintNodesByCondition")
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: newScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	return c, cfg
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met within 15s")
}
