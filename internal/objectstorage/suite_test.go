package objectstorage

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

var (
	testClient client.Client
	testConfig *rest.Config
	testScheme *runtime.Scheme
)

const unlabeledNamespace = "unlabeled"

const leavingNamespace = "leaving-prod"

var projectNamespaces = map[string][2]string{"acme-prod": {"acme", "prod"}, "beta-prod": {"beta", "prod"}, leavingNamespace: {"leaving", "prod"}}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	testScheme = runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(testScheme); err != nil {
		panic(err)
	}
	if err := v1alpha1.AddToScheme(testScheme); err != nil {
		panic(err)
	}
	env := &envtest.Environment{CRDDirectoryPaths: testenv.CRDPaths(), ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	defer func() { _ = env.Stop() }()
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	testConfig = cfg
	testClient, err = client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		panic(err)
	}
	if err := createNamespaces(context.Background()); err != nil {
		panic(err)
	}
	return m.Run()
}

func createNamespaces(ctx context.Context) error {
	create := func(name string, labels map[string]string) error {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
		return testClient.Create(ctx, ns)
	}
	if err := create(StoreNamespace, nil); err != nil {
		return err
	}
	if err := create(unlabeledNamespace, nil); err != nil {
		return err
	}
	for name, ids := range projectNamespaces {
		if err := create(name, map[string]string{v1alpha1.LabelTenant: ids[0], v1alpha1.LabelProject: ids[1]}); err != nil {
			return err
		}
	}
	return nil
}

func allNamespaces() []string {
	names := []string{StoreNamespace, unlabeledNamespace}
	for name := range projectNamespaces {
		names = append(names, name)
	}
	return names
}

func fresh(t *testing.T) {
	t.Helper()
	wipe(t)
	t.Cleanup(func() { wipe(t) })
}

func wipe(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, namespace := range allNamespaces() {
		wipeUsers(ctx, t, namespace)
		wipeBuckets(ctx, t, namespace)
		wipeKind(ctx, t, namespace, &corev1.Secret{})
		wipeKind(ctx, t, namespace, &corev1.ConfigMap{})
		for _, gvk := range []schema.GroupVersionKind{cephObjectStoreGVK, cephObjectStoreUserGVK, cephObjectStoreAccountGVK, objectBucketClaimGVK} {
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(gvk)
			wipeKind(ctx, t, namespace, obj)
		}
	}
}

func wipeUsers(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()
	var users v1alpha1.ObjectStorageUserList
	if err := testClient.List(ctx, &users, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	for i := range users.Items {
		user := users.Items[i]
		user.Finalizers = nil
		if err := testClient.Update(ctx, &user); err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		if err := testClient.Delete(ctx, &user); err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
	}
}

func wipeBuckets(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()
	var buckets v1alpha1.ObjectStorageBucketList
	if err := testClient.List(ctx, &buckets, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	for i := range buckets.Items {
		bucket := buckets.Items[i]
		bucket.Finalizers = nil
		if err := testClient.Update(ctx, &bucket); err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		if err := testClient.Delete(ctx, &bucket); err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
	}
}

func wipeKind(ctx context.Context, t *testing.T, namespace string, obj client.Object) {
	t.Helper()
	err := testClient.DeleteAllOf(ctx, obj, client.InNamespace(namespace))
	if err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) && !apierrors.IsMethodNotSupported(err) {
		t.Fatal(err)
	}
}
