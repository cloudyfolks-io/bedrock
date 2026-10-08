package images

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

func testScheme(t *testing.T) *runtime.Scheme {
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

func startEnvWithCRDs(t *testing.T, paths []string) (client.Client, *rest.Config) {
	t.Helper()
	env := &envtest.Environment{CRDDirectoryPaths: paths, ErrorIfCRDPathMissing: true}
	env.ControlPlane.GetAPIServer().Configure().Append("disable-admission-plugins", "TaintNodesByCondition")
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: testScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	return c, cfg
}

func startEnv(t *testing.T) client.Client {
	t.Helper()
	c, _ := startEnvWithCRDs(t, testenv.CRDPaths())
	return c
}

func ensureNamespace(t *testing.T, c client.Client, name string, labels map[string]string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
}

func tenantNamespace(t *testing.T, c client.Client, name string) {
	t.Helper()
	ensureNamespace(t, c, name, map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "web"})
}

func createClaim(t *testing.T, c client.Client, namespace, name, size string) *corev1.PersistentVolumeClaim {
	t.Helper()
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}},
		},
	}
	if err := c.Create(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	return claim
}

func fetch(t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func isGone(c client.Client, gvk schema.GroupVersionKind, namespace, name string) bool {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj))
}

func writeStatus(t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string, status map[string]any) {
	t.Helper()
	obj := fetch(t, c, gvk, namespace, name)
	obj.Object["status"] = status
	if err := c.Status().Update(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
}

func writeObjectStatus(t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string, status map[string]any) {
	t.Helper()
	obj := fetch(t, c, gvk, namespace, name)
	obj.Object["status"] = status
	if err := c.Update(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
}

func simulateImportSucceeded(t *testing.T, c client.Client, namespace, name string) *corev1.PersistentVolumeClaim {
	t.Helper()
	writeStatus(t, c, DataVolumeGVK, namespace, name, map[string]any{"phase": "Succeeded"})
	return createClaim(t, c, namespace, name, "10Gi")
}

func markSnapshotReady(t *testing.T, c client.Client, namespace, name, size string) {
	t.Helper()
	writeStatus(t, c, VolumeSnapshotGVK, namespace, name, map[string]any{"readyToUse": true, "restoreSize": size})
}

func markDataSourceReady(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	writeObjectStatus(t, c, DataSourceGVK, namespace, name, map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}},
	})
}
