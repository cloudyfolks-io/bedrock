package objectstorage

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

func reconcileUserWith(t *testing.T, r *UserReconciler, namespace, name string) {
	t.Helper()
	request := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
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

func countRook(t *testing.T, gvk schema.GroupVersionKind, namespace string) int {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := testClient.List(context.Background(), list, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func readSecret(t *testing.T, namespace, name string) (*corev1.Secret, bool) {
	t.Helper()
	var secret corev1.Secret
	err := testClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &secret)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return &secret, true
}

func TestUserWaitsForTheStore(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://unused.invalid")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "StoreNotInstalled")
	createStore(t, "Progressing")
	reconcileUserWith(t, r, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "StoreNotReady")
	if countRook(t, cephObjectStoreUserGVK, StoreNamespace) != 0 || countRook(t, cephObjectStoreAccountGVK, StoreNamespace) != 0 {
		t.Fatal("a Rook object exists")
	}
}

func TestUserWaitsForTheAccountAndTheRookUser(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://unused.invalid")
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "AccountPending")
	if countRook(t, cephObjectStoreUserGVK, StoreNamespace) != 0 {
		t.Fatal("a Rook user exists before the account is ready")
	}
	readyAccountOf(t, "acme-prod")
	reconcileUserWith(t, r, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "UserPending")
	rgw, found := getRookUser(t, "acme-prod.app")
	if !found {
		t.Fatal("no Rook user")
	}
	setRookPhase(t, rgw, "Ready")
	reconcileUserWith(t, r, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "CredentialsMissing")
}

func TestUserMakesTheRookUserInTheRookNamespace(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://unused.invalid")
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read", DisplayName: "App"})
	reconcileUserWith(t, r, "acme-prod", "app")
	readyAccountOf(t, "acme-prod")
	reconcileUserWith(t, r, "acme-prod", "app")
	rgw, found := getRookUser(t, "acme-prod.app")
	if !found || countRook(t, cephObjectStoreUserGVK, "acme-prod") != 0 {
		t.Fatal("the Rook user is not only in the Rook namespace")
	}
	if len(rgw.GetOwnerReferences()) != 0 {
		t.Fatalf("owner references %+v", rgw.GetOwnerReferences())
	}
	user := readUser(t, "acme-prod", "app")
	want := map[string]any{
		"store":       "bedrock-rgw",
		"displayName": "acme-prod.app",
		"accountRef":  map[string]any{"name": "acme-prod"},
	}
	if !reflect.DeepEqual(rgw.Object["spec"], want) {
		t.Fatalf("spec %+v", rgw.Object["spec"])
	}
	labels := rgw.GetLabels()
	if labels["bedrock.cloudyfolks.io/kind"] != "ObjectStorageUser" || labels["bedrock.cloudyfolks.io/name"] != "app" || labels["bedrock.cloudyfolks.io/tenant"] != "acme" || labels["bedrock.cloudyfolks.io/project"] != "prod" {
		t.Fatalf("labels %+v", labels)
	}
	if rgw.GetAnnotations()["bedrock.cloudyfolks.io/owner-uid"] != string(user.UID) || rgw.GetAnnotations()["bedrock.cloudyfolks.io/owner-namespace"] != "acme-prod" {
		t.Fatalf("annotations %+v", rgw.GetAnnotations())
	}
}

func TestUserCopiesOnlyItsKeysAndWritesThePolicy(t *testing.T) {
	fresh(t)
	iam, url := newFakeIAM(t)
	r := userReconcilerAt(url)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	readyUser(t, r, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "readWrite", Buckets: []string{"data"}})
	user := readUser(t, "acme-prod", "app")
	requireCondition(t, user.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionTrue, "Ready")
	if user.Status.CredentialsSecret != "app-s3" || user.Status.ARN != "arn:aws:iam::RGW00000000000000001:user/acme-prod.app" {
		t.Fatalf("status %+v", user.Status)
	}
	secret, found := readSecret(t, "acme-prod", "app-s3")
	if !found {
		t.Fatal("no Secret app-s3")
	}
	if string(secret.Data["AccessKey"]) != "AK" || string(secret.Data["SecretKey"]) != "SK" || string(secret.Data["Endpoint"]) != url {
		t.Fatalf("secret data %q", secret.Data)
	}
	if len(secret.Data) != 3 || secret.Labels["bedrock.cloudyfolks.io/kind"] != "ObjectStorageUser" || !metav1.IsControlledBy(secret, user) {
		t.Fatalf("secret %+v", secret.ObjectMeta)
	}
	var secrets corev1.SecretList
	if err := testClient.List(context.Background(), &secrets, client.InNamespace("acme-prod")); err != nil {
		t.Fatal(err)
	}
	for _, other := range secrets.Items {
		if string(other.Data["AccessKey"]) == "ROOTAK" || string(other.Data["SecretKey"]) == "ROOTSK" {
			t.Fatalf("the root key reached the project namespace in %s", other.Name)
		}
	}
	want, err := UserPolicy("readWrite", []string{"acme-prod-data"})
	if err != nil {
		t.Fatal(err)
	}
	if got := iam.policy("acme-prod.app"); got != want {
		t.Fatalf("policy %s", got)
	}
}

func TestUserSkipsThePutWhenThePolicyIsCurrent(t *testing.T) {
	fresh(t)
	iam, url := newFakeIAM(t)
	r := userReconcilerAt(url)
	readyUser(t, r, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	iam.take()
	reconcileUserWith(t, r, "acme-prod", "app")
	if actions := iam.take(); !reflect.DeepEqual(actions, []string{"GetUserPolicy acme-prod.app"}) {
		t.Fatalf("actions %v", actions)
	}
}

func TestUserPolicyFollowsTheBucketsOfTheProject(t *testing.T) {
	fresh(t)
	iam, url := newFakeIAM(t)
	r := userReconcilerAt(url)
	readyUser(t, r, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	only, _ := UserPolicy("read", nil)
	if got := iam.policy("acme-prod.app"); got != only {
		t.Fatalf("policy %s", got)
	}
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	createBucket(t, "acme-prod", "logs", v1alpha1.ObjectStorageBucketSpec{BucketName: "acme-logs"})
	createBucket(t, "beta-prod", "other", v1alpha1.ObjectStorageBucketSpec{})
	reconcileUserWith(t, r, "acme-prod", "app")
	both, _ := UserPolicy("read", []string{"acme-prod-data", "acme-logs"})
	if got := iam.policy("acme-prod.app"); got != both {
		t.Fatalf("policy after create %s", got)
	}
	if err := testClient.Delete(context.Background(), &v1alpha1.ObjectStorageBucket{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "acme-prod"}}); err != nil {
		t.Fatal(err)
	}
	reconcileUserWith(t, r, "acme-prod", "app")
	logsOnly, _ := UserPolicy("read", []string{"acme-logs"})
	if got := iam.policy("acme-prod.app"); got != logsOnly {
		t.Fatalf("policy after delete %s", got)
	}
}

func TestUserPolicyListsOnlyTheNamedBuckets(t *testing.T) {
	fresh(t)
	iam, url := newFakeIAM(t)
	r := userReconcilerAt(url)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	createBucket(t, "acme-prod", "logs", v1alpha1.ObjectStorageBucketSpec{})
	readyUser(t, r, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read", Buckets: []string{"logs", "missing"}})
	want, _ := UserPolicy("read", []string{"acme-prod-logs"})
	if got := iam.policy("acme-prod.app"); got != want {
		t.Fatalf("policy %s", got)
	}
}

func TestUserSuspendAndResume(t *testing.T) {
	fresh(t)
	_, url := newFakeIAM(t)
	r := userReconcilerAt(url)
	readyUser(t, r, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	if mask, found, _ := unstructured.NestedSlice(mustRookUser(t, "acme-prod.app").Object, "spec", "opMask"); found {
		t.Fatalf("opMask %v before suspend", mask)
	}
	user := readUser(t, "acme-prod", "app")
	user.Spec.Suspended = true
	if err := testClient.Update(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	reconcileUserWith(t, r, "acme-prod", "app")
	mask, found, _ := unstructured.NestedSlice(mustRookUser(t, "acme-prod.app").Object, "spec", "opMask")
	if !found || len(mask) != 0 {
		t.Fatalf("opMask %v found %v", mask, found)
	}
	user = readUser(t, "acme-prod", "app")
	user.Spec.Suspended = false
	if err := testClient.Update(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	reconcileUserWith(t, r, "acme-prod", "app")
	if mask, found, _ := unstructured.NestedSlice(mustRookUser(t, "acme-prod.app").Object, "spec", "opMask"); found {
		t.Fatalf("opMask %v after resume", mask)
	}
}

func mustRookUser(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	obj, found := getRookUser(t, name)
	if !found {
		t.Fatalf("no Rook user %s", name)
	}
	return obj
}

func TestUserDeleteRemovesPolicyRookUserAndSecret(t *testing.T) {
	fresh(t)
	iam, url := newFakeIAM(t)
	r := userReconcilerAt(url)
	readyUser(t, r, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	if err := testClient.Delete(context.Background(), readUser(t, "acme-prod", "app")); err != nil {
		t.Fatal(err)
	}
	iam.take()
	reconcileUserWith(t, r, "acme-prod", "app")
	if iam.policy("acme-prod.app") != "" {
		t.Fatal("the policy is still stored")
	}
	if _, found := getRookUser(t, "acme-prod.app"); found {
		t.Fatal("the Rook user is still there")
	}
	if _, found := readSecret(t, "acme-prod", "app-s3"); found {
		t.Fatal("the Secret is still there")
	}
	var gone v1alpha1.ObjectStorageUser
	err := testClient.Get(context.Background(), client.ObjectKey{Namespace: "acme-prod", Name: "app"}, &gone)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("user still there: %v", err)
	}
	if _, found, _ := getRook(context.Background(), testClient, cephObjectStoreAccountGVK, client.ObjectKey{Namespace: StoreNamespace, Name: "acme-prod"}); !found {
		t.Fatal("the last user removed the account")
	}
}

func TestUserDeleteWithoutAnAccountStillFinishes(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://unused.invalid")
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	if err := testClient.Delete(context.Background(), readUser(t, "acme-prod", "app")); err != nil {
		t.Fatal(err)
	}
	reconcileUserWith(t, r, "acme-prod", "app")
	var gone v1alpha1.ObjectStorageUser
	if err := testClient.Get(context.Background(), client.ObjectKey{Namespace: "acme-prod", Name: "app"}, &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("user still there: %v", err)
	}
}

func TestUserWithoutAnAccountRefIsLegacy(t *testing.T) {
	fresh(t)
	_, url := newFakeIAM(t)
	r := userReconcilerAt(url)
	createStore(t, "Ready")
	legacy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreUser",
		"metadata":   map[string]any{"name": "acme-prod.app", "namespace": StoreNamespace},
		"spec":       map[string]any{"store": StoreName},
	}}
	if err := testClient.Create(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "LegacyUser")
	if countRook(t, cephObjectStoreAccountGVK, StoreNamespace) != 0 {
		t.Fatal("an account was made for a legacy user")
	}
	if err := testClient.Delete(context.Background(), readUser(t, "acme-prod", "app")); err != nil {
		t.Fatal(err)
	}
	reconcileUserWith(t, r, "acme-prod", "app")
	if _, found := getRookUser(t, "acme-prod.app"); !found {
		t.Fatal("the legacy Rook user was deleted")
	}
}

func TestUserInTheProjectNamespaceIsLegacy(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://unused.invalid")
	createStore(t, "Ready")
	old := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreUser",
		"metadata":   map[string]any{"name": "acme-prod.app", "namespace": "acme-prod"},
		"spec":       map[string]any{"store": StoreName},
	}}
	if err := testClient.Create(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "LegacyUser")
}

func TestUserSameNameInTwoProjects(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://unused.invalid")
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	createUser(t, "beta-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	reconcileUserWith(t, r, "beta-prod", "app")
	readyAccountOf(t, "acme-prod")
	readyAccountOf(t, "beta-prod")
	reconcileUserWith(t, r, "acme-prod", "app")
	reconcileUserWith(t, r, "beta-prod", "app")
	if _, found := getRookUser(t, "acme-prod.app"); !found {
		t.Fatal("no acme-prod.app")
	}
	if _, found := getRookUser(t, "beta-prod.app"); !found {
		t.Fatal("no beta-prod.app")
	}
	if countRook(t, cephObjectStoreUserGVK, StoreNamespace) != 2 {
		t.Fatal("the Rook namespace holds more than the two users")
	}
}

func TestUserOutsideAProjectNamespace(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://unused.invalid")
	createStore(t, "Ready")
	createUser(t, unlabeledNamespace, "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, unlabeledNamespace, "app")
	requireUserReason(t, unlabeledNamespace, "app", metav1.ConditionFalse, "NamespaceNotProject")
}

func TestUserReportsAnUnavailableStore(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://127.0.0.1:1")
	readyUserWithoutPolicy(t, r)
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "StoreNotReady")
}

func readyUserWithoutPolicy(t *testing.T, r *UserReconciler) {
	t.Helper()
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	readyAccountOf(t, "acme-prod")
	reconcileUserWith(t, r, "acme-prod", "app")
	makeRookUserReady(t, "acme-prod.app")
	reconcileUserWith(t, r, "acme-prod", "app")
}

func TestUserStatusIsWrittenByTheAreaManager(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://unused.invalid")
	createStore(t, "Ready")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	for _, entry := range readUser(t, "acme-prod", "app").ManagedFields {
		if entry.Subresource == "status" && entry.Manager == v1alpha1.ObjectStorageFieldManager {
			return
		}
	}
	t.Fatalf("managed fields %+v", readUser(t, "acme-prod", "app").ManagedFields)
}

func TestBucketEventsEnqueueEveryUserOfTheNamespace(t *testing.T) {
	fresh(t)
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	createUser(t, "acme-prod", "ci", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	createUser(t, "beta-prod", "other", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	r := userReconcilerAt("http://unused.invalid")
	requests := r.usersOfBucket(context.Background(), &v1alpha1.ObjectStorageBucket{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "acme-prod"}})
	got := map[string]bool{}
	for _, request := range requests {
		got[request.String()] = true
	}
	if len(got) != 2 || !got["acme-prod/app"] || !got["acme-prod/ci"] {
		t.Fatalf("requests %v", got)
	}
}
