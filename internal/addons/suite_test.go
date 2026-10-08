package addons

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

var (
	testConfig *rest.Config
	testScheme *runtime.Scheme
	testBundle release.Bundle
)

func TestMain(m *testing.M) {
	testScheme = runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(testScheme); err != nil {
		panic(err)
	}
	if err := v1alpha1.AddToScheme(testScheme); err != nil {
		panic(err)
	}
	built, err := os.MkdirTemp("", "addons-release-")
	if err != nil {
		panic(err)
	}
	testBundle = buildBundle(built)
	env := &envtest.Environment{CRDDirectoryPaths: testenv.CRDPaths(), ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	testConfig = cfg
	setup, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		panic(err)
	}
	for _, name := range []string{"bedrock-system", "monitoring"} {
		if err := setup.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil && !apierrors.IsAlreadyExists(err) {
			panic(err)
		}
	}
	code := m.Run()
	_ = env.Stop()
	_ = os.RemoveAll(built)
	os.Exit(code)
}

func buildBundle(out string) release.Bundle {
	cfg, err := release.LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		panic(err)
	}
	cfg.Components = componentsNamed(cfg.Components, Kata, Loki)
	opts := release.BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-io/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("..", ".."), CacheDir: out}
	if err := release.Build(cfg, opts); err != nil {
		panic(err)
	}
	bundle, err := release.Load(os.DirFS(out))
	if err != nil {
		panic(err)
	}
	return bundle
}

func newClient(t *testing.T) client.Client {
	t.Helper()
	c, err := client.New(testConfig, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
