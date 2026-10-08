package network

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

var (
	envOnce   sync.Once
	envClient client.Client
	envConfig *rest.Config
	envServer *envtest.Environment
	envErr    error
	counter   atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if envServer != nil {
		_ = envServer.Stop()
	}
	os.Exit(code)
}

func testScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return scheme, v1alpha1.AddToScheme(scheme)
}

func testEnv(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	envOnce.Do(func() {
		scheme, err := testScheme()
		if err != nil {
			envErr = err
			return
		}
		envServer = &envtest.Environment{CRDDirectoryPaths: testenv.CRDPaths(), ErrorIfCRDPathMissing: true}
		cfg, err := envServer.Start()
		if err != nil {
			envErr = err
			return
		}
		cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
		envConfig = cfg
		envClient, envErr = client.New(cfg, client.Options{Scheme: scheme})
	})
	if envErr != nil {
		t.Fatal(envErr)
	}
	return envClient, envConfig
}

func unique(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s%d", prefix, counter.Add(1))
}

func newNamespace(t *testing.T, tenant, project string) string {
	t.Helper()
	c, _ := testEnv(t)
	name := tenant + "-" + project
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{v1alpha1.LabelTenant: tenant, v1alpha1.LabelProject: project}}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestEnvironmentStartsWithAllCRDs(t *testing.T) {
	c, _ := testEnv(t)
	namespace := newNamespace(t, "suite", unique(t, "p"))
	if namespace == "" || c == nil {
		t.Fatal("namespace was not created")
	}
}

func setSetting(t *testing.T, key, value string) {
	t.Helper()
	c, _ := testEnv(t)
	ctx := context.Background()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}}
	err := c.Get(ctx, client.ObjectKey{Name: key}, setting)
	switch {
	case apierrors.IsNotFound(err):
		setting.Spec.Value = value
		err = c.Create(ctx, setting)
	case err == nil:
		setting.Spec.Value = value
		err = c.Update(ctx, setting)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}}) })
}
