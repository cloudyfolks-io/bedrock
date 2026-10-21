package objectstorage

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func setNamespaceAnnotation(t *testing.T, namespace, key, value string) {
	t.Helper()
	var ns corev1.Namespace
	if err := testClient.Get(context.Background(), client.ObjectKey{Name: namespace}, &ns); err != nil {
		t.Fatal(err)
	}
	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	ns.Annotations[key] = value
	if err := testClient.Update(context.Background(), &ns); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var latest corev1.Namespace
		if err := testClient.Get(context.Background(), client.ObjectKey{Name: namespace}, &latest); err != nil {
			t.Fatal(err)
		}
		delete(latest.Annotations, key)
		if err := testClient.Update(context.Background(), &latest); err != nil {
			t.Fatal(err)
		}
	})
}

func adoptAccountName(t *testing.T, namespace, name string) {
	t.Helper()
	setNamespaceAnnotation(t, namespace, AccountNameAnnotation, name)
}

func TestLoadProjectReadsTheAdoptedAccountName(t *testing.T) {
	adoptAccountName(t, "acme-prod", "production-acme")
	if got := loadedProject(t, "acme-prod").AdoptedAccount; got != "production-acme" {
		t.Fatalf("got %q", got)
	}
	if got := loadedProject(t, "beta-prod").AdoptedAccount; got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestAccountObjectCarriesTheAdoptedAccountName(t *testing.T) {
	got := AccountObject(Project{Namespace: "acme-prod", Tenant: "acme", Name: "prod", AdoptedAccount: "production-acme"})
	name, _, _ := unstructured.NestedString(got.Object, "spec", "name")
	if name != "production-acme" || got.GetName() != "acme-prod" {
		t.Fatalf("spec name %q object %q", name, got.GetName())
	}
	plain := AccountObject(Project{Namespace: "acme-prod", Tenant: "acme", Name: "prod"})
	if _, found, _ := unstructured.NestedString(plain.Object, "spec", "name"); found {
		t.Fatal("a plain account has a name")
	}
}

func TestEnsureAccountAdoptsAFreeAccountName(t *testing.T) {
	fresh(t)
	adoptAccountName(t, "acme-prod", "production-acme")
	if _, _, err := EnsureAccount(context.Background(), testClient, loadedProject(t, "acme-prod")); err != nil {
		t.Fatal(err)
	}
	name, _, _ := unstructured.NestedString(getAccount(t, "acme-prod").Object, "spec", "name")
	if name != "production-acme" {
		t.Fatalf("spec name %q", name)
	}
}

func TestEnsureAccountRefusesANameAnotherProjectHolds(t *testing.T) {
	fresh(t)
	if _, _, err := EnsureAccount(context.Background(), testClient, loadedProject(t, "beta-prod")); err != nil {
		t.Fatal(err)
	}
	adoptAccountName(t, "acme-prod", "beta-prod")
	_, _, err := EnsureAccount(context.Background(), testClient, loadedProject(t, "acme-prod"))
	if !errors.Is(err, ErrAccountClaimed) {
		t.Fatalf("err %v", err)
	}
	if countRook(t, cephObjectStoreAccountGVK, StoreNamespace) != 1 {
		t.Fatal("a second account was made")
	}
}

func TestEnsureAccountRefusesANameAnotherAdoptionHolds(t *testing.T) {
	fresh(t)
	adoptAccountName(t, "beta-prod", "production-shared")
	if _, _, err := EnsureAccount(context.Background(), testClient, loadedProject(t, "beta-prod")); err != nil {
		t.Fatal(err)
	}
	adoptAccountName(t, "acme-prod", "production-shared")
	if _, _, err := EnsureAccount(context.Background(), testClient, loadedProject(t, "acme-prod")); !errors.Is(err, ErrAccountClaimed) {
		t.Fatalf("err %v", err)
	}
}

func TestEnsureAccountKeepsAnAccountTheProjectOwnsAlready(t *testing.T) {
	fresh(t)
	adoptAccountName(t, "acme-prod", "production-acme")
	project := loadedProject(t, "acme-prod")
	for range 2 {
		if _, _, err := EnsureAccount(context.Background(), testClient, project); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUserReportsAClaimedAccountName(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	if _, _, err := EnsureAccount(context.Background(), testClient, loadedProject(t, "beta-prod")); err != nil {
		t.Fatal(err)
	}
	adoptAccountName(t, "acme-prod", "beta-prod")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	reconcileUserWith(t, userReconcilerAt("http://unused.invalid"), "acme-prod", "app")
	requireUserReason(t, "acme-prod", "app", metav1.ConditionFalse, "AccountNameTaken")
	if countRook(t, cephObjectStoreUserGVK, StoreNamespace) != 0 {
		t.Fatal("a Rook user exists")
	}
}

func TestBucketReportsAClaimedAccountName(t *testing.T) {
	fresh(t)
	_, url := newFakeS3(t)
	createStore(t, "Ready")
	if _, _, err := EnsureAccount(context.Background(), testClient, loadedProject(t, "beta-prod")); err != nil {
		t.Fatal(err)
	}
	adoptAccountName(t, "acme-prod", "beta-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, bucketReconciler(url), "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "AccountNameTaken")
}

func TestBucketOfAnAdoptedAccountIsBoundNotTaken(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	store.fill("old-assets", 2)
	adoptAccountName(t, "acme-prod", "production-acme")
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{BucketName: "old-assets"})
	settle(t, bucketReconciler(url), "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionTrue, "Bound")
	if readBucket(t, "acme-prod", "data").Status.BucketName != "old-assets" || len(store.objects["old-assets"]) != 2 {
		t.Fatal("the adopted bucket is not bound")
	}
}
