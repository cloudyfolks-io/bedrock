package database

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

var rookUserGVK = schema.GroupVersionKind{Group: "ceph.rook.io", Version: "v1", Kind: "CephObjectStoreUser"}

func ensureNamespace(t testing.TB, c client.Client, name string) {
	t.Helper()
	err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

func deleteBackupIdentity(t testing.TB, c client.Client) {
	t.Helper()
	ctx := context.Background()
	rook := &unstructured.Unstructured{}
	rook.SetGroupVersionKind(rookUserGVK)
	rook.SetNamespace(objectstorage.StoreNamespace)
	rook.SetName(BackupSecretName)
	_ = c.Delete(ctx, rook)
	for _, secret := range []client.ObjectKey{
		{Namespace: objectstorage.StoreNamespace, Name: platform.StoreUserSecretName(objectstorage.Store(), BackupSecretName)},
		{Namespace: PlatformNamespace, Name: BackupSecretName},
	} {
		_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: secret.Namespace, Name: secret.Name}})
	}
}

func makeBackupUserReady(t testing.TB, c client.Client, accessKey, secretKey string) {
	t.Helper()
	ctx := context.Background()
	rook := &unstructured.Unstructured{}
	rook.SetGroupVersionKind(rookUserGVK)
	must(t, c.Get(ctx, client.ObjectKey{Namespace: objectstorage.StoreNamespace, Name: BackupSecretName}, rook))
	must(t, unstructured.SetNestedField(rook.Object, "Ready", "status", "phase"))
	must(t, c.Status().Update(ctx, rook))
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: objectstorage.StoreNamespace, Name: platform.StoreUserSecretName(objectstorage.Store(), BackupSecretName)},
		Data:       map[string][]byte{"AccessKey": []byte(accessKey), "SecretKey": []byte(secretKey)},
	}
	must(t, c.Create(ctx, secret))
}
