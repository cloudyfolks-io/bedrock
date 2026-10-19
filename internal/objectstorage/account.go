package objectstorage

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

type Account struct {
	ID   string
	Root s3.Credentials
}

func AccountName(namespace string) string {
	return namespace
}

func AccountObject(project Project) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreAccount",
		"metadata":   map[string]any{"name": AccountName(project.Namespace), "namespace": StoreNamespace},
		"spec":       map[string]any{"store": StoreName},
	}}
	return platformAdopt(obj, project)
}

func EnsureAccount(ctx context.Context, c client.Client, project Project) (Account, bool, error) {
	if err := ssa.Apply(ctx, c, AccountObject(project), v1alpha1.ObjectStorageFieldManager); err != nil {
		return Account{}, false, err
	}
	return ReadAccount(ctx, c, project.Namespace)
}

func ReadAccount(ctx context.Context, c client.Client, namespace string) (Account, bool, error) {
	rook, found, err := getRook(ctx, c, cephObjectStoreAccountGVK, client.ObjectKey{Namespace: StoreNamespace, Name: AccountName(namespace)})
	if err != nil || !found || rookPhase(rook) != "Ready" {
		return Account{}, false, err
	}
	id, _, _ := unstructured.NestedString(rook.Object, "status", "accountID")
	secretName, _, _ := unstructured.NestedString(rook.Object, "status", "rootAccountSecretName")
	if id == "" || secretName == "" {
		return Account{}, false, nil
	}
	var secret corev1.Secret
	err = c.Get(ctx, client.ObjectKey{Namespace: StoreNamespace, Name: secretName}, &secret)
	if apierrors.IsNotFound(err) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	root := s3.Credentials{AccessKey: string(secret.Data[AccessKeyKey]), SecretKey: string(secret.Data[SecretKeyKey])}
	if root.AccessKey == "" || root.SecretKey == "" {
		return Account{}, false, nil
	}
	return Account{ID: id, Root: root}, true, nil
}

func accountPending() outcome {
	return outcome{reason: reasonAccountPending, message: "the object store has not made the account of this project yet", requeue: pendingRequeue}
}
