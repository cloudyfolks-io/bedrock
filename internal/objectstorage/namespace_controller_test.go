package objectstorage

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

func ownedRook(t *testing.T, apiVersionKind [2]string, name string, owner platform.Owner) {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersionKind[0],
		"kind":       apiVersionKind[1],
		"metadata":   map[string]any{"name": name, "namespace": StoreNamespace},
		"spec":       map[string]any{"store": StoreName},
	}}
	if err := testClient.Create(context.Background(), platform.Adopt(obj, owner)); err != nil {
		t.Fatal(err)
	}
}

func goneProjectObjects(t *testing.T) {
	t.Helper()
	ownedRook(t, [2]string{"ceph.rook.io/v1", "CephObjectStoreAccount"}, "gone-prod", platform.Owner{Kind: "Namespace", Name: "gone-prod", UID: types.UID("old"), Tenant: "gone", Project: "prod"})
	ownedRook(t, [2]string{"ceph.rook.io/v1", "CephObjectStoreUser"}, "gone-prod.app", platform.Owner{Kind: "ObjectStorageUser", Name: "app", Namespace: "gone-prod", UID: types.UID("old"), Tenant: "gone", Project: "prod"})
	ownedRook(t, [2]string{"ceph.rook.io/v1", "CephObjectStoreUser"}, "acme-prod.app", platform.Owner{Kind: "ObjectStorageUser", Name: "app", Namespace: "acme-prod", UID: types.UID("live"), Tenant: "acme", Project: "prod"})
	ownedRook(t, [2]string{"ceph.rook.io/v1", "CephObjectStoreAccount"}, "acme-prod", platform.Owner{Kind: "Namespace", Name: "acme-prod", UID: types.UID("live"), Tenant: "acme", Project: "prod"})
	ownedRook(t, [2]string{"ceph.rook.io/v1", "CephObjectStoreUser"}, "bedrock-admin", platform.Owner{})
}

func reconcileNamespace(t *testing.T, name string) ctrl.Result {
	t.Helper()
	result, err := (&NamespaceReconciler{Client: testClient}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func rookNames(t *testing.T, kind string) map[string]bool {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(cephObjectStoreUserGVK.GroupVersion().WithKind(kind + "List"))
	if err := testClient.List(context.Background(), list, client.InNamespace(StoreNamespace)); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, item := range list.Items {
		names[item.GetName()] = true
	}
	return names
}

func TestNamespaceGoneReleasesItsAccountAndUsers(t *testing.T) {
	fresh(t)
	goneProjectObjects(t)
	if result := reconcileNamespace(t, "gone-prod"); result.RequeueAfter != 0 {
		t.Fatalf("result %+v", result)
	}
	users, accounts := rookNames(t, "CephObjectStoreUser"), rookNames(t, "CephObjectStoreAccount")
	if users["gone-prod.app"] || accounts["gone-prod"] {
		t.Fatalf("left behind: %v %v", users, accounts)
	}
	if !users["acme-prod.app"] || !users["bedrock-admin"] || !accounts["acme-prod"] {
		t.Fatalf("removed too much: %v %v", users, accounts)
	}
}

func TestNamespaceThatIsAliveKeepsEverything(t *testing.T) {
	fresh(t)
	goneProjectObjects(t)
	reconcileNamespace(t, "acme-prod")
	if !rookNames(t, "CephObjectStoreAccount")["acme-prod"] || !rookNames(t, "CephObjectStoreUser")["acme-prod.app"] {
		t.Fatal("a live project lost its objects")
	}
}

func TestNamespaceWaitsForTheTenantObjects(t *testing.T) {
	fresh(t)
	goneProjectObjects(t)
	createNamespaceNamed(t, "gone-prod")
	createUser(t, "gone-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	deleteNamespaceRecord(t, "gone-prod")
	if result := reconcileNamespace(t, "gone-prod"); result.RequeueAfter == 0 {
		t.Fatalf("result %+v", result)
	}
	if !rookNames(t, "CephObjectStoreAccount")["gone-prod"] {
		t.Fatal("the account was removed while a user remained")
	}
	if err := testClient.Delete(context.Background(), &v1alpha1.ObjectStorageUser{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "gone-prod"}}); err != nil {
		t.Fatal(err)
	}
	if result := reconcileNamespace(t, "gone-prod"); result.RequeueAfter != 0 {
		t.Fatalf("result %+v", result)
	}
	if rookNames(t, "CephObjectStoreAccount")["gone-prod"] {
		t.Fatal("the account remains")
	}
}

func createNamespaceNamed(t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := testClient.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testClient.Delete(context.Background(), ns) })
}

func deleteNamespaceRecord(t *testing.T, name string) {
	t.Helper()
	if err := testClient.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerExists(t *testing.T) {
	fresh(t)
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	user := readUser(t, "acme-prod", "app")
	project := loadedProject(t, "acme-prod")
	exists := OwnerExists(testClient)
	cases := []struct {
		name  string
		owner platform.Owner
		want  bool
	}{
		{"live user", platform.Owner{Kind: "ObjectStorageUser", Name: "app", Namespace: "acme-prod", UID: user.UID}, true},
		{"recreated user", platform.Owner{Kind: "ObjectStorageUser", Name: "app", Namespace: "acme-prod", UID: types.UID("old")}, false},
		{"missing user", platform.Owner{Kind: "ObjectStorageUser", Name: "none", Namespace: "acme-prod", UID: user.UID}, false},
		{"live namespace", platform.Owner{Kind: "Namespace", Name: "acme-prod", UID: project.UID}, true},
		{"recreated namespace", platform.Owner{Kind: "Namespace", Name: "acme-prod", UID: types.UID("old")}, false},
		{"missing namespace", platform.Owner{Kind: "Namespace", Name: "gone-prod", UID: project.UID}, false},
		{"unknown kind", platform.Owner{Kind: "Other", Name: "x"}, true},
	}
	for _, tc := range cases {
		got, err := exists(context.Background(), tc.owner)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %v %v", tc.name, got, err)
		}
	}
}

func TestSweeperDryRunKeepsOrphans(t *testing.T) {
	fresh(t)
	goneProjectObjects(t)
	(&Sweeper{Client: testClient, DryRun: true}).sweep(context.Background())
	if !rookNames(t, "CephObjectStoreUser")["gone-prod.app"] {
		t.Fatal("a dry run removed an orphan")
	}
	(&Sweeper{Client: testClient, DryRun: false}).sweep(context.Background())
	users := rookNames(t, "CephObjectStoreUser")
	if users["gone-prod.app"] || !users["bedrock-admin"] {
		t.Fatalf("users %v", users)
	}
}
