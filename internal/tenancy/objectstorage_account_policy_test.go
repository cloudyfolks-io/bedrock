package tenancy

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	accountAnnotation = "bedrock.cloudyfolks.io/object-storage-account"
	accountMessage    = "only an administrator may set the object storage account name, and it cannot change afterwards"
)

func accountNamespace(name, account string) *corev1.Namespace {
	ns := namespaceNamed(name, nil)
	if account != "" {
		ns.Annotations = map[string]string{accountAnnotation: account}
	}
	return ns
}

func accountWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "namespace-writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"create", "update", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "namespace-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "namespace-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
	)
	applyFile(t, admin, manifestPath("86-tenancy", "82-objectstorage-account-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	if err := eventuallyForbidden(t, denyCreate(admin, alice, accountNamespace("probe", "dev-acme"))); err == nil {
		t.Fatal("the account name policy never denied")
	}
	return admin, alice, root
}

func TestNonAdminCannotSetTheAccountName(t *testing.T) {
	_, alice, _ := accountWorld(t)
	err := alice.Create(context.Background(), accountNamespace("t-one", "dev-acme"))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), accountMessage) {
		t.Fatalf("create: err = %v", err)
	}
	plain := accountNamespace("t-two", "")
	mustCreate(t, alice, plain)
	plain.Annotations = map[string]string{accountAnnotation: "dev-acme"}
	err = alice.Update(context.Background(), plain)
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), accountMessage) {
		t.Fatalf("update: err = %v", err)
	}
}

func TestAdminSetsTheAccountNameOnce(t *testing.T) {
	_, _, root := accountWorld(t)
	ns := accountNamespace("t-three", "")
	mustCreate(t, root, ns)
	ns.Annotations = map[string]string{accountAnnotation: "dev-acme"}
	if err := root.Update(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, root, accountNamespace("t-four", "dev-beta"))
}

func TestTheAccountNameCannotChangeOrDisappear(t *testing.T) {
	admin, alice, root := accountWorld(t)
	held := accountNamespace("t-held", "dev-acme")
	mustCreate(t, admin, held)
	for name, actor := range map[string]client.Client{"alice": alice, "root": root} {
		for label, mutate := range map[string]func(*corev1.Namespace){
			"change": func(ns *corev1.Namespace) { ns.Annotations[accountAnnotation] = "dev-other" },
			"remove": func(ns *corev1.Namespace) { delete(ns.Annotations, accountAnnotation) },
		} {
			var latest corev1.Namespace
			if err := admin.Get(context.Background(), client.ObjectKey{Name: "t-held"}, &latest); err != nil {
				t.Fatal(err)
			}
			mutate(&latest)
			err := actor.Update(context.Background(), &latest)
			if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), accountMessage) {
				t.Fatalf("%s %s: err = %v", name, label, err)
			}
		}
	}
}

func TestTheAccountNameKeepsOtherNamespaceChanges(t *testing.T) {
	admin, alice, _ := accountWorld(t)
	held := accountNamespace("t-kept", "dev-acme")
	mustCreate(t, admin, held)
	var latest corev1.Namespace
	if err := admin.Get(context.Background(), client.ObjectKey{Name: "t-kept"}, &latest); err != nil {
		t.Fatal(err)
	}
	latest.Annotations["note"] = "one"
	if err := alice.Update(context.Background(), &latest); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}

func TestTheAccountNameRefusesOddCharacters(t *testing.T) {
	_, _, root := accountWorld(t)
	err := root.Create(context.Background(), accountNamespace("t-odd", "dev acme/x"))
	if !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v", err)
	}
}
