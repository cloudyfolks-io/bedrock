package objectstorage

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

func createAdoptingUser(t *testing.T, namespace, name, rgwUser string, spec v1alpha1.ObjectStorageUserSpec) {
	t.Helper()
	user := &v1alpha1.ObjectStorageUser{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: map[string]string{AdoptUserAnnotation: rgwUser}}, Spec: spec}
	if err := testClient.Create(context.Background(), user); err != nil {
		t.Fatal(err)
	}
}

func adoptionSetup(t *testing.T, known ...string) (*fakeIAM, *UserReconciler) {
	t.Helper()
	fresh(t)
	iam, url := newFakeIAM(t)
	for _, name := range known {
		iam.known[name] = true
	}
	createStore(t, "Ready")
	return iam, userReconcilerAt(url)
}

func TestUserAdoptsAnExistingRGWUserAndKeepsItsKeys(t *testing.T) {
	iam, r := adoptionSetup(t, "k-8f3a")
	createAdoptingUser(t, "acme-prod", "app", "k-8f3a", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	readyAccountOf(t, "acme-prod")
	reconcileUserWith(t, r, "acme-prod", "app")
	rgw, found := getRookUser(t, "k-8f3a")
	if !found {
		t.Fatal("no Rook user named like the adopted user")
	}
	if name, _, _ := unstructured.NestedString(rgw.Object, "spec", "accountRef", "name"); name != "acme-prod" {
		t.Fatalf("accountRef %q", name)
	}
	if _, found := getRookUser(t, "acme-prod.app"); found {
		t.Fatal("a second Rook user was made")
	}
	setRookPhase(t, rgw, "Ready")
	createRookKeys(t, "k-8f3a", map[string][]byte{"AccessKey": []byte("OLDAK"), "SecretKey": []byte("OLDSK")})
	reconcileUserWith(t, r, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionTrue, "Ready")
	secret, found := readSecret(t, "acme-prod", "app-s3")
	if !found || string(secret.Data["AccessKey"]) != "OLDAK" || string(secret.Data["SecretKey"]) != "OLDSK" {
		t.Fatalf("secret %+v", secret)
	}
	if iam.policy("k-8f3a") == "" || iam.policy("acme-prod.app") != "" {
		t.Fatal("the policy is not on the adopted user")
	}
	if got := readUser(t, "acme-prod", "app").Status.ARN; got != "arn:aws:iam::RGW00000000000000001:user/k-8f3a" {
		t.Fatalf("arn %q", got)
	}
}

func TestUserAdoptionRefusesAUserTheAccountDoesNotHold(t *testing.T) {
	iam, r := adoptionSetup(t)
	createAdoptingUser(t, "acme-prod", "app", "k-other", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	readyAccountOf(t, "acme-prod")
	reconcileUserWith(t, r, "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "AdoptUserNotFound")
	if countRook(t, cephObjectStoreUserGVK, StoreNamespace) != 0 {
		t.Fatal("a Rook user exists")
	}
	if !slices.Contains(iam.take(), "GetUser k-other") {
		t.Fatal("the backend was not asked")
	}
}

func TestUserAdoptionRefusesAUserAnotherObjectHolds(t *testing.T) {
	_, r := adoptionSetup(t, "k-8f3a")
	owner := platform.Owner{Kind: "ObjectStorageUser", Name: "first", Namespace: "acme-prod", UID: "other-uid", Tenant: "acme", Project: "prod"}
	held := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1", "kind": "CephObjectStoreUser",
		"metadata": map[string]any{"name": "k-8f3a", "namespace": StoreNamespace},
		"spec":     map[string]any{"store": StoreName, "accountRef": map[string]any{"name": "acme-prod"}},
	}}
	if err := testClient.Create(context.Background(), platform.Adopt(held, owner)); err != nil {
		t.Fatal(err)
	}
	createAdoptingUser(t, "acme-prod", "second", "k-8f3a", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "second")
	readyAccountOf(t, "acme-prod")
	reconcileUserWith(t, r, "acme-prod", "second")
	requireUserReason(t, "acme-prod", "second", metav1.ConditionFalse, "UserClaimed")
	rgw, _ := getRookUser(t, "k-8f3a")
	if rgw.GetLabels()["bedrock.cloudyfolks.io/name"] != "first" {
		t.Fatal("the held Rook user changed")
	}
}

func TestUserWithoutTheAnnotationNeverAsksForTheUser(t *testing.T) {
	iam, r := adoptionSetup(t, "acme-prod.app")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	readyAccountOf(t, "acme-prod")
	reconcileUserWith(t, r, "acme-prod", "app")
	if slices.Contains(iam.take(), "GetUser acme-prod.app") {
		t.Fatal("a plain user was checked as an adoption")
	}
}

func TestAdoptedUserKeepsItsOwnershipOnLaterPasses(t *testing.T) {
	iam, r := adoptionSetup(t, "k-8f3a")
	createAdoptingUser(t, "acme-prod", "app", "k-8f3a", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	readyAccountOf(t, "acme-prod")
	reconcileUserWith(t, r, "acme-prod", "app")
	iam.take()
	reconcileUserWith(t, r, "acme-prod", "app")
	if slices.Contains(iam.take(), "GetUser k-8f3a") {
		t.Fatal("an adopted user is checked again")
	}
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "UserPending")
}

func TestAdoptedUserDeleteRemovesTheAdoptedRookUser(t *testing.T) {
	_, r := adoptionSetup(t, "k-8f3a")
	createAdoptingUser(t, "acme-prod", "app", "k-8f3a", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, r, "acme-prod", "app")
	readyAccountOf(t, "acme-prod")
	reconcileUserWith(t, r, "acme-prod", "app")
	makeRookUserReady(t, "k-8f3a")
	reconcileUserWith(t, r, "acme-prod", "app")
	if err := testClient.Delete(context.Background(), readUser(t, "acme-prod", "app")); err != nil {
		t.Fatal(err)
	}
	reconcileUserWith(t, r, "acme-prod", "app")
	if _, found := getRookUser(t, "k-8f3a"); found {
		t.Fatal("the adopted Rook user is still there")
	}
	var gone v1alpha1.ObjectStorageUser
	if err := testClient.Get(context.Background(), client.ObjectKey{Namespace: "acme-prod", Name: "app"}, &gone); err == nil {
		t.Fatal("the user is still there")
	}
}
