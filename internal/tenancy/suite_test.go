package tenancy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
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

var envSlots = make(chan struct{}, 3)

func startEnvWithPaths(t *testing.T, paths []string) (client.Client, *rest.Config) {
	t.Helper()
	t.Parallel()
	envSlots <- struct{}{}
	t.Cleanup(func() { <-envSlots })
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
	err := wait.PollUntilContextTimeout(context.Background(), 100*time.Millisecond, 15*time.Second, true, func(context.Context) (bool, error) {
		return ok(), nil
	})
	if err != nil {
		t.Fatal("condition not met within 15s")
	}
}

func requireNever(t *testing.T, window time.Duration, violated func() bool) {
	t.Helper()
	_ = wait.PollUntilContextTimeout(context.Background(), 100*time.Millisecond, window, true, func(context.Context) (bool, error) {
		if violated() {
			t.Fatal("a forbidden state was reached")
		}
		return false, nil
	})
}

func actingAs(t *testing.T, cfg *rest.Config, user string, groups ...string) client.Client {
	t.Helper()
	impersonated := rest.CopyConfig(cfg)
	impersonated.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: groups}
	c, err := client.New(impersonated, client.Options{Scheme: newScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func applyFile(t *testing.T, c client.Client, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		if len(obj.Object) == 0 {
			continue
		}
		if err := c.Create(context.Background(), &obj); err != nil {
			t.Fatalf("create %s %s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
}

func manifestPath(group, file string) string {
	return filepath.Join("..", "..", "manifests", group, file)
}

func mustCreate(t *testing.T, c client.Client, objs ...client.Object) {
	t.Helper()
	for _, obj := range objs {
		if err := c.Create(context.Background(), obj); err != nil {
			t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
		}
	}
}

func namespaceNamed(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func startManager(t *testing.T, cfg *rest.Config, setup func(ctrl.Manager) error) context.Context {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     newScheme(t),
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := setup(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mgr.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return ctx
}

func startEnvWithoutAAQ(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	paths := slices.DeleteFunc(testenv.CRDPaths(), func(path string) bool { return filepath.Base(path) == "aaq" })
	return startEnvWithPaths(t, paths)
}
