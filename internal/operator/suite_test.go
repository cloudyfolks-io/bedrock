package operator

import (
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func StartTestEnv(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	return startTestEnvWithCRDs(t)
}

func startTestEnvWithCRDs(t *testing.T, dirs ...string) (client.Client, *rest.Config) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     append([]string{filepath.Join("..", "..", "manifests", "00-crds")}, dirs...),
		ErrorIfCRDPathMissing: true,
	}
	env.ControlPlane.GetAPIServer().Configure().Append("disable-admission-plugins", "TaintNodesByCondition")
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c, cfg
}

func newTestScheme(t *testing.T) *runtime.Scheme {
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

func metricsDisabled() metricsserver.Options {
	return metricsserver.Options{BindAddress: "0"}
}

func testManagerOptions(t *testing.T) ctrl.Options {
	t.Helper()
	return ctrl.Options{
		Scheme:     newTestScheme(t),
		Metrics:    metricsDisabled(),
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
		Client:     client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.ConfigMap{}}}},
	}
}
