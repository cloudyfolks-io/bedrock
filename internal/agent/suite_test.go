package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var (
	k8sClient client.Client
	cfg       *rest.Config
)

func TestMain(m *testing.M) {
	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runSuite(m *testing.M) (int, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return 0, err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return 0, err
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "manifests", "00-crds")},
		ErrorIfCRDPathMissing: true,
	}
	env.ControlPlane.GetAPIServer().Configure().Append("disable-admission-plugins", "TaintNodesByCondition")
	restConfig, err := env.Start()
	if err != nil {
		return 0, err
	}
	defer func() { _ = env.Stop() }()
	cfg = restConfig
	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return 0, err
	}
	k8sClient = c
	return m.Run(), nil
}
