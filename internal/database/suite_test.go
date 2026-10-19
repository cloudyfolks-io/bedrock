package database

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

var (
	sharedEnv    *envtest.Environment
	sharedOnce   sync.Once
	sharedConfig *rest.Config
	sharedErr    error
	sequence     int
	sequenceMu   sync.Mutex
)

func testScheme(t testing.TB) *runtime.Scheme {
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

func startEnv(t testing.TB) client.Client {
	t.Helper()
	sharedOnce.Do(func() {
		sharedEnv = &envtest.Environment{CRDDirectoryPaths: testenv.CRDPaths(), ErrorIfCRDPathMissing: true}
		sharedConfig, sharedErr = sharedEnv.Start()
		if sharedErr == nil {
			sharedConfig.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
		}
	})
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	c, err := client.New(sharedConfig, client.Options{Scheme: testScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedEnv != nil {
		_ = sharedEnv.Stop()
	}
	os.Exit(code)
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func create(t testing.TB, c client.Client, obj client.Object) {
	t.Helper()
	must(t, c.Create(context.Background(), obj))
}

func nextSequence() string {
	sequenceMu.Lock()
	defer sequenceMu.Unlock()
	sequence++
	return strconv.Itoa(sequence)
}

func projectNamespace(t testing.TB, c client.Client) string {
	t.Helper()
	name := "acme-web-" + nextSequence()
	create(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "web"},
	}})
	return name
}

func setSetting(t testing.TB, c client.Client, key, value string) {
	t.Helper()
	ctx := context.Background()
	must(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var setting v1alpha1.Setting
		err := c.Get(ctx, client.ObjectKey{Name: key}, &setting)
		if apierrors.IsNotFound(err) {
			return c.Create(ctx, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}, Spec: v1alpha1.SettingSpec{Value: value}})
		}
		if err != nil {
			return err
		}
		setting.Spec.Value = value
		return c.Update(ctx, &setting)
	}))
	t.Cleanup(func() { _ = c.Delete(ctx, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}}) })
}

func setAddon(t testing.TB, c client.Client, name string, enabled bool) {
	t.Helper()
	ctx := context.Background()
	must(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var addon v1alpha1.Addon
		err := c.Get(ctx, client.ObjectKey{Name: name}, &addon)
		if apierrors.IsNotFound(err) {
			return c.Create(ctx, &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.AddonSpec{Enabled: enabled}})
		}
		if err != nil {
			return err
		}
		addon.Spec.Enabled = enabled
		return c.Update(ctx, &addon)
	}))
	t.Cleanup(func() { _ = c.Delete(ctx, &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: name}}) })
}

func eventually(t testing.TB, what string, check func() bool) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), 100*time.Millisecond, 20*time.Second, true, func(context.Context) (bool, error) { return check(), nil })
	if err != nil {
		t.Fatalf("timed out waiting for %s", what)
	}
}

func reconcileRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKey{Name: PlatformNamespace}}
}
