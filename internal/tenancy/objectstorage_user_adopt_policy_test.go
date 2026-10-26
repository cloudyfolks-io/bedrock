package tenancy

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	adoptUserAnnotation = "bedrock.cloudyfolks.io/adopt-rgw-user"
	adoptUserMessage    = "only an administrator may set the adopt-rgw-user annotation, when the user is created, and it cannot change afterwards"
)

func adoptingUser(name, rgwUser string) *v1alpha1.ObjectStorageUser {
	user := &v1alpha1.ObjectStorageUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "t-acme"},
		Spec:       v1alpha1.ObjectStorageUserSpec{Permission: "read"},
	}
	if rgwUser != "" {
		user.Annotations = map[string]string{adoptUserAnnotation: rgwUser}
	}
	return user
}

func userAdoptWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "user-writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"objectstorageusers"}, Verbs: []string{"create", "update", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "user-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "user-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "t-acme"}},
	)
	applyFile(t, admin, manifestPath("86-tenancy", "82-objectstorage-user-adopt-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	if err := eventuallyForbidden(t, denyCreate(admin, alice, adoptingUser("probe", "k-probe"))); err == nil {
		t.Fatal("the user adopt policy never denied")
	}
	return admin, alice, root
}

func TestNonAdminCannotAdoptAnRGWUser(t *testing.T) {
	_, alice, _ := userAdoptWorld(t)
	err := alice.Create(context.Background(), adoptingUser("a", "k-8f3a"))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), adoptUserMessage) {
		t.Fatalf("create: err = %v", err)
	}
	plain := adoptingUser("plain", "")
	mustCreate(t, alice, plain)
	plain.Annotations = map[string]string{adoptUserAnnotation: "k-8f3a"}
	err = alice.Update(context.Background(), plain)
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), adoptUserMessage) {
		t.Fatalf("update: err = %v", err)
	}
}

func TestAdminAdoptsAnRGWUserOnCreate(t *testing.T) {
	_, _, root := userAdoptWorld(t)
	mustCreate(t, root, adoptingUser("a", "k-8f3a"))
}

func TestAdminCannotAddOrChangeTheAdoptionLater(t *testing.T) {
	admin, alice, root := userAdoptWorld(t)
	plain := adoptingUser("plain", "")
	mustCreate(t, admin, plain)
	plain.Annotations = map[string]string{adoptUserAnnotation: "k-8f3a"}
	if err := root.Update(context.Background(), plain); !apierrors.IsForbidden(err) {
		t.Fatalf("add: err = %v", err)
	}
	held := adoptingUser("held", "k-1111")
	mustCreate(t, admin, held)
	for name, actor := range map[string]client.Client{"alice": alice, "root": root} {
		var latest v1alpha1.ObjectStorageUser
		if err := admin.Get(context.Background(), client.ObjectKeyFromObject(held), &latest); err != nil {
			t.Fatal(err)
		}
		latest.Annotations = nil
		if err := actor.Update(context.Background(), &latest); !apierrors.IsForbidden(err) {
			t.Fatalf("%s remove: err = %v", name, err)
		}
	}
}

func TestAdoptedUserKeepsOtherChanges(t *testing.T) {
	admin, alice, _ := userAdoptWorld(t)
	mustCreate(t, admin, adoptingUser("held", "k-1111"))
	var latest v1alpha1.ObjectStorageUser
	if err := admin.Get(context.Background(), client.ObjectKey{Namespace: "t-acme", Name: "held"}, &latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.Suspended = true
	if err := alice.Update(context.Background(), &latest); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}

func TestAdoptedUserNameRefusesOddCharacters(t *testing.T) {
	_, _, root := userAdoptWorld(t)
	err := root.Create(context.Background(), adoptingUser("odd", "K_8F3A/x"))
	if !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v", err)
	}
}
