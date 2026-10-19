package objectstorage

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

func TestAccountNameIsTheNamespace(t *testing.T) {
	if got := AccountName("acme-prod"); got != "acme-prod" {
		t.Fatalf("got %q", got)
	}
}

func TestAccountObject(t *testing.T) {
	got := AccountObject(Project{Namespace: "acme-prod", Tenant: "acme", Name: "prod", UID: types.UID("uid-ns")})
	want := map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreAccount",
		"metadata": map[string]any{
			"name":      "acme-prod",
			"namespace": "rook-ceph",
			"labels": map[string]any{
				"bedrock.cloudyfolks.io/kind":    "Namespace",
				"bedrock.cloudyfolks.io/name":    "acme-prod",
				"bedrock.cloudyfolks.io/tenant":  "acme",
				"bedrock.cloudyfolks.io/project": "prod",
			},
			"annotations": map[string]any{"bedrock.cloudyfolks.io/owner-uid": "uid-ns"},
		},
		"spec": map[string]any{"store": "bedrock-rgw"},
	}
	if !reflect.DeepEqual(got.Object, want) {
		t.Fatalf("got %+v", got.Object)
	}
}

func loadedProject(t *testing.T, namespace string) Project {
	t.Helper()
	project, found, err := LoadProject(context.Background(), testClient, namespace)
	if err != nil || !found {
		t.Fatalf("project %v %v", found, err)
	}
	return project
}

func getAccount(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	account, found, err := getRook(context.Background(), testClient, cephObjectStoreAccountGVK, client.ObjectKey{Namespace: StoreNamespace, Name: name})
	if err != nil || !found {
		t.Fatalf("account %v %v", found, err)
	}
	return account
}

func makeAccountReady(t *testing.T, name string) {
	t.Helper()
	account := getAccount(t, name)
	if err := unstructured.SetNestedField(account.Object, "Ready", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(account.Object, "RGW00000000000000001", "status", "accountID"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(account.Object, name+"-root", "status", "rootAccountSecretName"); err != nil {
		t.Fatal(err)
	}
	if err := testClient.Status().Update(context.Background(), account); err != nil {
		t.Fatal(err)
	}
}

func createRootSecret(t *testing.T, name string) {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-root", Namespace: StoreNamespace},
		Data:       map[string][]byte{AccessKeyKey: []byte("ROOTAK"), SecretKeyKey: []byte("ROOTSK")},
	}
	if err := testClient.Create(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureAccountWaitsUntilRookIsReady(t *testing.T) {
	fresh(t)
	project := loadedProject(t, "acme-prod")
	_, ready, err := EnsureAccount(context.Background(), testClient, project)
	if err != nil || ready {
		t.Fatalf("ready %v err %v", ready, err)
	}
	account := getAccount(t, "acme-prod")
	if account.GetLabels()["bedrock.cloudyfolks.io/tenant"] != "acme" || len(account.GetOwnerReferences()) != 0 {
		t.Fatalf("account %+v", account.Object["metadata"])
	}
	if err := unstructured.SetNestedField(account.Object, "Progressing", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if err := testClient.Status().Update(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := EnsureAccount(context.Background(), testClient, project); err != nil || ready {
		t.Fatalf("ready %v err %v", ready, err)
	}
}

func TestEnsureAccountWaitsForTheRootSecret(t *testing.T) {
	fresh(t)
	project := loadedProject(t, "acme-prod")
	if _, _, err := EnsureAccount(context.Background(), testClient, project); err != nil {
		t.Fatal(err)
	}
	makeAccountReady(t, "acme-prod")
	if _, ready, err := EnsureAccount(context.Background(), testClient, project); err != nil || ready {
		t.Fatalf("ready %v err %v", ready, err)
	}
}

func TestEnsureAccountReturnsTheRootKeyAndId(t *testing.T) {
	fresh(t)
	project := loadedProject(t, "acme-prod")
	if _, _, err := EnsureAccount(context.Background(), testClient, project); err != nil {
		t.Fatal(err)
	}
	makeAccountReady(t, "acme-prod")
	createRootSecret(t, "acme-prod")
	account, ready, err := EnsureAccount(context.Background(), testClient, project)
	if err != nil || !ready {
		t.Fatalf("ready %v err %v", ready, err)
	}
	if account.ID != "RGW00000000000000001" || account.Root != (s3.Credentials{AccessKey: "ROOTAK", SecretKey: "ROOTSK"}) {
		t.Fatalf("account %+v", account)
	}
}

func TestEnsureAccountIsIdempotent(t *testing.T) {
	fresh(t)
	project := loadedProject(t, "acme-prod")
	if _, _, err := EnsureAccount(context.Background(), testClient, project); err != nil {
		t.Fatal(err)
	}
	before := getAccount(t, "acme-prod").GetResourceVersion()
	if _, _, err := EnsureAccount(context.Background(), testClient, project); err != nil {
		t.Fatal(err)
	}
	if getAccount(t, "acme-prod").GetResourceVersion() != before {
		t.Fatal("the account changed")
	}
}

func TestLoadProjectReadsLabelsAndUid(t *testing.T) {
	project := loadedProject(t, "acme-prod")
	if project.Tenant != "acme" || project.Name != "prod" || project.Namespace != "acme-prod" || project.UID == "" {
		t.Fatalf("project %+v", project)
	}
	if _, found, err := LoadProject(context.Background(), testClient, unlabeledNamespace); err != nil || found {
		t.Fatalf("found %v err %v", found, err)
	}
	if _, found, err := LoadProject(context.Background(), testClient, "missing"); err != nil || found {
		t.Fatalf("found %v err %v", found, err)
	}
}
