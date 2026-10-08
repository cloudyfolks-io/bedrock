package objectstorage

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func createStore(t *testing.T, phase string) {
	t.Helper()
	store := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStore",
		"metadata":   map[string]any{"name": StoreName, "namespace": StoreNamespace},
		"spec":       map[string]any{},
	}}
	if err := testClient.Create(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	setRookPhase(t, store, phase)
}

func setRookPhase(t *testing.T, obj *unstructured.Unstructured, phase string) {
	t.Helper()
	if err := unstructured.SetNestedField(obj.Object, phase, "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if err := testClient.Status().Update(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
}

func createUser(t *testing.T, namespace, name string, spec v1alpha1.ObjectStorageUserSpec) {
	t.Helper()
	user := &v1alpha1.ObjectStorageUser{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
	if err := testClient.Create(context.Background(), user); err != nil {
		t.Fatal(err)
	}
}

func createBucket(t *testing.T, namespace, name string, spec v1alpha1.ObjectStorageBucketSpec) {
	t.Helper()
	bucket := &v1alpha1.ObjectStorageBucket{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
	if err := testClient.Create(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
}

func userReconciler() *UserReconciler {
	return &UserReconciler{Client: testClient}
}

func reconcileUser(t *testing.T, namespace, name string) {
	t.Helper()
	request := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}}
	if _, err := userReconciler().Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func readUser(t *testing.T, namespace, name string) *v1alpha1.ObjectStorageUser {
	t.Helper()
	var user v1alpha1.ObjectStorageUser
	if err := testClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &user); err != nil {
		t.Fatal(err)
	}
	return &user
}

func requireCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	found := meta.FindStatusCondition(conditions, conditionType)
	if found == nil || found.Status != status || found.Reason != reason {
		t.Fatalf("condition %s want %s %s, have %+v", conditionType, status, reason, found)
	}
}

func requireUserReason(t *testing.T, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	requireCondition(t, readUser(t, namespace, name).Status.Conditions, v1alpha1.ConditionReady, status, reason)
}

func getCephUser(t *testing.T, namespace, name string) (*unstructured.Unstructured, bool) {
	t.Helper()
	obj, found, err := getRook(context.Background(), testClient, cephObjectStoreUserGVK, client.ObjectKey{Namespace: namespace, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return obj, found
}

func countRook(t *testing.T, gvk schema.GroupVersionKind, namespace string) int {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := testClient.List(context.Background(), list, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func createCredentials(t *testing.T, namespace, rgwUser string, data map[string][]byte) {
	t.Helper()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CredentialsSecretName(rgwUser), Namespace: namespace}, Data: data}
	if err := testClient.Create(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
}

func fullCredentials() map[string][]byte {
	return map[string][]byte{"AccessKey": []byte("AK"), "SecretKey": []byte("SK"), "Endpoint": []byte("http://s3.test")}
}

func TestUserWaitsForTheStore(t *testing.T) {
	fresh(t)
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{})
	reconcileUser(t, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "StoreNotInstalled")
	createStore(t, "Progressing")
	reconcileUser(t, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "StoreNotReady")
	if countRook(t, cephObjectStoreUserGVK, "acme-prod") != 0 {
		t.Fatal("a Rook user exists")
	}
}

func TestUserCreatesTheCephUser(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	quota := v1alpha1.ObjectStorageUserQuota{Bytes: resource.NewQuantity(1073741824, resource.BinarySI), Buckets: int32Ptr(5)}
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{DisplayName: "App", Quota: quota})
	reconcileUser(t, "acme-prod", "app")
	rgw, found := getCephUser(t, "acme-prod", "acme-prod.app")
	if !found {
		t.Fatal("no Rook user")
	}
	store, _, _ := unstructured.NestedString(rgw.Object, "spec", "store")
	display, _, _ := unstructured.NestedString(rgw.Object, "spec", "displayName")
	buckets, _, _ := unstructured.NestedInt64(rgw.Object, "spec", "quotas", "maxBuckets")
	if store != "bedrock-rgw" || display != "App" || buckets != 5 {
		t.Fatalf("spec %+v", rgw.Object["spec"])
	}
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "UserPending")
}

func TestUserReportsTheCredentialsSecret(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{})
	reconcileUser(t, "acme-prod", "app")
	rgw, _ := getCephUser(t, "acme-prod", "acme-prod.app")
	setRookPhase(t, rgw, "Ready")
	reconcileUser(t, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "CredentialsMissing")
	data := fullCredentials()
	delete(data, "SecretKey")
	createCredentials(t, "acme-prod", "acme-prod.app", data)
	reconcileUser(t, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "CredentialsMissing")
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: "acme-prod", Name: CredentialsSecretName("acme-prod.app")}
	if err := testClient.Get(context.Background(), key, &secret); err != nil {
		t.Fatal(err)
	}
	secret.Data = fullCredentials()
	if err := testClient.Update(context.Background(), &secret); err != nil {
		t.Fatal(err)
	}
	reconcileUser(t, "acme-prod", "app")
	user := readUser(t, "acme-prod", "app")
	requireCondition(t, user.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionTrue, "Ready")
	if user.Status.CredentialsSecret != "rook-ceph-object-user-bedrock-rgw-acme-prod.app" {
		t.Fatalf("secret %q", user.Status.CredentialsSecret)
	}
}

func TestUserSameNameInTwoProjects(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{})
	createUser(t, "beta-prod", "app", v1alpha1.ObjectStorageUserSpec{})
	reconcileUser(t, "acme-prod", "app")
	reconcileUser(t, "beta-prod", "app")
	if _, found := getCephUser(t, "acme-prod", "acme-prod.app"); !found {
		t.Fatal("no acme-prod.app")
	}
	if _, found := getCephUser(t, "beta-prod", "beta-prod.app"); !found {
		t.Fatal("no beta-prod.app")
	}
	if countRook(t, cephObjectStoreUserGVK, "acme-prod") != 1 || countRook(t, cephObjectStoreUserGVK, "beta-prod") != 1 {
		t.Fatal("a namespace holds more than its own user")
	}
}

func TestUserQuotaChangeIsApplied(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Quota: v1alpha1.ObjectStorageUserQuota{Buckets: int32Ptr(5)}})
	reconcileUser(t, "acme-prod", "app")
	user := readUser(t, "acme-prod", "app")
	user.Spec.Quota.Buckets = int32Ptr(8)
	if err := testClient.Update(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	reconcileUser(t, "acme-prod", "app")
	rgw, _ := getCephUser(t, "acme-prod", "acme-prod.app")
	buckets, _, _ := unstructured.NestedInt64(rgw.Object, "spec", "quotas", "maxBuckets")
	if buckets != 8 {
		t.Fatalf("maxBuckets %d", buckets)
	}
}

func TestUserRefusesANegativeQuota(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Quota: v1alpha1.ObjectStorageUserQuota{Bytes: resource.NewQuantity(-5, resource.BinarySI)}})
	reconcileUser(t, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "QuotaInvalid")
	if countRook(t, cephObjectStoreUserGVK, "acme-prod") != 0 {
		t.Fatal("a Rook user exists")
	}
}

func TestUserDeletionWaitsForBuckets(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{})
	reconcileUser(t, "acme-prod", "app")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{UserRef: "app"})
	if err := testClient.Delete(context.Background(), readUser(t, "acme-prod", "app")); err != nil {
		t.Fatal(err)
	}
	reconcileUser(t, "acme-prod", "app")
	user := readUser(t, "acme-prod", "app")
	if user.DeletionTimestamp.IsZero() || len(user.Finalizers) != 1 {
		t.Fatalf("user %+v", user.ObjectMeta)
	}
	requireCondition(t, user.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, "InUse")
	bucket := &v1alpha1.ObjectStorageBucket{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "acme-prod"}}
	if err := testClient.Delete(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileUser(t, "acme-prod", "app")
	var gone v1alpha1.ObjectStorageUser
	err := testClient.Get(context.Background(), client.ObjectKey{Namespace: "acme-prod", Name: "app"}, &gone)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("user still there: %v", err)
	}
}

func TestUserOutsideAProjectNamespace(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	createUser(t, unlabeledNamespace, "app", v1alpha1.ObjectStorageUserSpec{})
	reconcileUser(t, unlabeledNamespace, "app")
	requireUserReason(t, unlabeledNamespace, "app", metav1.ConditionFalse, "NamespaceNotProject")
}

func TestUserStatusIsWrittenByTheAreaManager(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{})
	reconcileUser(t, "acme-prod", "app")
	for _, entry := range readUser(t, "acme-prod", "app").ManagedFields {
		if entry.Subresource == "status" && entry.Manager == v1alpha1.ObjectStorageFieldManager {
			return
		}
	}
	t.Fatalf("managed fields %+v", readUser(t, "acme-prod", "app").ManagedFields)
}
