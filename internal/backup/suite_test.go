package backup

import (
	"context"
	"fmt"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

var (
	k8sClient client.Client
	cfg       *rest.Config
	scheme    *runtime.Scheme
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
	scheme = runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return 0, err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return 0, err
	}
	env := &envtest.Environment{CRDDirectoryPaths: testenv.CRDPaths(), ErrorIfCRDPathMissing: true}
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
	for _, ns := range []string{SystemNamespace, VeleroNamespace} {
		if err := ensureNamespace(context.Background(), ns, nil); err != nil {
			return 0, err
		}
	}
	return m.Run(), nil
}

func ensureNamespace(ctx context.Context, name string, labels map[string]string) error {
	err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
