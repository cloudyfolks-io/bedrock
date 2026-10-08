package images

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

func fixedNow() time.Time {
	return testNow
}

func httpImage(name, namespace string) *v1alpha1.Image {
	return &v1alpha1.Image{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       httpSpec("https://example.com/rocky.qcow2"),
	}
}

func TestImageReconcilerWritesStatus(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	img := httpImage("rocky", "acme-web")
	if err := c.Create(context.Background(), img); err != nil {
		t.Fatal(err)
	}

	r := &ImageReconciler{Client: c, Now: fixedNow}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(img)})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}

	var got v1alpha1.Image
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(img), &got); err != nil {
		t.Fatal(err)
	}
	cond := got.Status.Conditions[0]
	if got.Status.Phase != "Pending" || cond.Type != "Ready" || cond.Status != metav1.ConditionFalse || cond.Reason != "ImportScheduled" || got.Status.ObservedGeneration != got.Generation {
		t.Fatalf("status %+v", got.Status)
	}
	dv := fetch(t, c, DataVolumeGVK, "acme-web", "rocky-import")
	if dv.GetLabels()["bedrock.cloudyfolks.io/tenant"] != "acme" || dv.GetLabels()["bedrock.cloudyfolks.io/project"] != "web" {
		t.Fatalf("labels %v", dv.GetLabels())
	}
}

func TestClusterImageReconcilerUsesTheCatalogNamespace(t *testing.T) {
	c := startEnv(t)
	ensureNamespace(t, c, "bedrock-images", nil)
	img := &v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "rocky-9"}, Spec: httpSpec("https://example.com/rocky.qcow2")}
	if err := c.Create(context.Background(), img); err != nil {
		t.Fatal(err)
	}

	r := &ClusterImageReconciler{Client: c, Now: fixedNow}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(img)}); err != nil {
		t.Fatal(err)
	}
	fetch(t, c, DataVolumeGVK, "bedrock-images", "rocky-9-import")
	var got v1alpha1.ClusterImage
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(img), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Pending" {
		t.Fatalf("status %+v", got.Status)
	}
}

func TestReconcileIgnoresDeletedImages(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	r := &ImageReconciler{Client: c, Now: fixedNow}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "acme-web", Name: "gone"}})
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
	cluster := &ClusterImageReconciler{Client: c, Now: fixedNow}
	if result, err := cluster.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "gone"}}); err != nil || result.RequeueAfter != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
}

func fakeWithStatusUpdates(t *testing.T, img *v1alpha1.Image, updates *atomic.Int32, conflicts int32) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(img).WithStatusSubresource(img).WithInterceptorFuncs(interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if updates.Add(1) <= conflicts {
				return apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("images").GroupResource(), obj.GetName(), nil)
			}
			return c.SubResource(subResource).Update(ctx, obj, opts...)
		},
	}).Build()
}

func TestWriteStatusRetriesOnConflict(t *testing.T) {
	img := httpImage("rocky", "acme-web")
	var updates atomic.Int32
	c := fakeWithStatusUpdates(t, img, &updates, 1)

	if err := writeStatus(context.Background(), c, img.DeepCopy(), imageStatus, Result{Phase: "Pending", Reason: "ImportScheduled"}, testNow); err != nil {
		t.Fatal(err)
	}
	if updates.Load() != 2 {
		t.Fatalf("updates %d", updates.Load())
	}
	var got v1alpha1.Image
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(img), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Pending" {
		t.Fatalf("status %+v", got.Status)
	}
}

func TestWriteStatusSkipsWhenUnchanged(t *testing.T) {
	img := httpImage("rocky", "acme-web")
	var updates atomic.Int32
	c := fakeWithStatusUpdates(t, img, &updates, 0)
	result := Result{Phase: "Pending", Reason: "ImportScheduled"}

	for range 2 {
		if err := writeStatus(context.Background(), c, img.DeepCopy(), imageStatus, result, testNow); err != nil {
			t.Fatal(err)
		}
	}
	if updates.Load() != 1 {
		t.Fatalf("updates %d", updates.Load())
	}
}

func managerFor(t *testing.T, cfg *rest.Config) ctrl.Manager {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     testScheme(t),
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestSetupSkipsWithoutCDIKinds(t *testing.T) {
	_, cfg := startEnvWithCRDs(t, testenv.CRDPaths()[:1])
	if err := SetupWithManager(managerFor(t, cfg)); err != nil {
		t.Fatal(err)
	}
}

func TestManagerReconcilesAnImage(t *testing.T) {
	c, cfg := startEnvWithCRDs(t, testenv.CRDPaths())
	mgr := managerFor(t, cfg)
	if err := SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()

	tenantNamespace(t, c, "acme-web")
	img := httpImage("rocky", "acme-web")
	if err := c.Create(ctx, img); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.Image
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKeyFromObject(img), &got); err != nil {
			return false, nil
		}
		return got.Status.Phase == "Pending" && !isGone(c, DataVolumeGVK, "acme-web", "rocky-import"), nil
	})
	if err != nil {
		t.Fatalf("image was not reconciled: %v %+v", err, got.Status)
	}
}
