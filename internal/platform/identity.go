package platform

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	accessKeyField = "AccessKey"
	secretKeyField = "SecretKey"
)

var cephObjectStoreUserGVK = schema.GroupVersionKind{Group: "ceph.rook.io", Version: "v1", Kind: "CephObjectStoreUser"}

type Store struct {
	Name      string
	Namespace string
}

type Identity struct {
	Store           Store
	Name            string
	TargetNamespace string
	AccessKeyNames  []string
	SecretKeyNames  []string
	Capabilities    map[string]any
	Manager         string
}

func StoreUserSecretName(store Store, name string) string {
	return "rook-ceph-object-user-" + store.Name + "-" + name
}

func StoreUserObject(store Store, name string, capabilities map[string]any) *unstructured.Unstructured {
	spec := map[string]any{"store": store.Name, "displayName": name}
	if len(capabilities) > 0 {
		spec["capabilities"] = capabilities
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreUser",
		"metadata":   map[string]any{"name": name, "namespace": store.Namespace},
		"spec":       spec,
	}}
}

func EnsureStoreUser(ctx context.Context, c client.Client, store Store, name, manager string, capabilities map[string]any) (s3.Credentials, bool, error) {
	if err := ssa.Apply(ctx, c, StoreUserObject(store, name, capabilities), manager); err != nil {
		return s3.Credentials{}, false, err
	}
	rook := &unstructured.Unstructured{}
	rook.SetGroupVersionKind(cephObjectStoreUserGVK)
	err := c.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: name}, rook)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return s3.Credentials{}, false, nil
	}
	if err != nil {
		return s3.Credentials{}, false, err
	}
	if phase, _, _ := unstructured.NestedString(rook.Object, "status", "phase"); phase != "Ready" {
		return s3.Credentials{}, false, nil
	}
	var secret corev1.Secret
	err = c.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: StoreUserSecretName(store, name)}, &secret)
	if apierrors.IsNotFound(err) {
		return s3.Credentials{}, false, nil
	}
	if err != nil {
		return s3.Credentials{}, false, err
	}
	keys := s3.Credentials{AccessKey: string(secret.Data[accessKeyField]), SecretKey: string(secret.Data[secretKeyField])}
	return keys, keys.AccessKey != "" && keys.SecretKey != "", nil
}

func EnsureStoreIdentity(ctx context.Context, c client.Client, id Identity) (bool, error) {
	if id.TargetNamespace == "" || len(id.AccessKeyNames) == 0 || len(id.SecretKeyNames) == 0 {
		return false, errors.New("an identity needs a target namespace and key names")
	}
	keys, ready, err := EnsureStoreUser(ctx, c, id.Store, id.Name, id.Manager, id.Capabilities)
	if err != nil || !ready {
		return false, err
	}
	data := map[string][]byte{}
	for _, name := range id.AccessKeyNames {
		data[name] = []byte(keys.AccessKey)
	}
	for _, name := range id.SecretKeyNames {
		data[name] = []byte(keys.SecretKey)
	}
	secret := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: id.Name, Namespace: id.TargetNamespace},
		Data:       data,
	}
	return true, ssa.Apply(ctx, c, secret, id.Manager)
}

func EnsureStoreBucket(ctx context.Context, store *s3.Client, endpoint, region string, keys s3.Credentials, bucket string) error {
	err := store.CreateBucket(ctx, endpoint, region, keys, bucket)
	if err == nil || s3.IsCode(err, s3.BucketAlreadyOwnedByYou) {
		return nil
	}
	return err
}
