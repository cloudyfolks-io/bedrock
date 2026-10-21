package tenancy

import (
	"context"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const databaseAdoptMessage = "only an administrator may set the adopt annotation of a database user or schema"

func adoptingUser(name string, annotations map[string]string) *v1alpha1.DatabaseUser {
	return &v1alpha1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "t-acme", Annotations: annotations},
		Spec:       v1alpha1.DatabaseUserSpec{DatabaseRef: "orders"},
	}
}

func adoptingSchema(name string, annotations map[string]string) *v1alpha1.DatabaseSchema {
	return &v1alpha1.DatabaseSchema{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "t-acme", Annotations: annotations},
		Spec:       v1alpha1.DatabaseSchemaSpec{DatabaseRef: "orders"},
	}
}

func databaseAdoptWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "database-access-writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"databaseusers", "databaseschemas"}, Verbs: []string{"create", "update", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "database-access-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "database-access-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
		namespaceNamed("t-acme", nil),
	)
	applyFile(t, admin, manifestPath("86-tenancy", "90-database-adopt-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	probe := adoptingUser("probe", map[string]string{v1alpha1.AnnotationAdopt: "true"})
	if err := eventuallyForbidden(t, denyCreate(admin, alice, probe)); err == nil {
		t.Fatal("the database adopt policy never denied")
	}
	return admin, alice, root
}

func requireAdoptDenied(t *testing.T, err error, what string) {
	t.Helper()
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), databaseAdoptMessage) {
		t.Fatalf("%s: err = %v", what, err)
	}
}

func TestNonAdminCannotAdoptADatabaseUserOrSchema(t *testing.T) {
	_, alice, _ := databaseAdoptWorld(t)
	ctx := context.Background()
	annotated := map[string]string{v1alpha1.AnnotationAdopt: "true"}
	requireAdoptDenied(t, alice.Create(ctx, adoptingUser("u-create", annotated)), "user create")
	requireAdoptDenied(t, alice.Create(ctx, adoptingSchema("s-create", annotated)), "schema create")
	user, schema := adoptingUser("u-plain", nil), adoptingSchema("s-plain", nil)
	mustCreate(t, alice, user, schema)
	user.Annotations, schema.Annotations = annotated, annotated
	requireAdoptDenied(t, alice.Update(ctx, user), "user update")
	requireAdoptDenied(t, alice.Update(ctx, schema), "schema update")
}

func TestNonAdminCannotRemoveTheAdoptAnnotation(t *testing.T) {
	admin, alice, _ := databaseAdoptWorld(t)
	adopted := adoptingUser("u-adopted", map[string]string{v1alpha1.AnnotationAdopt: "true"})
	mustCreate(t, admin, adopted)
	adopted.Annotations = nil
	requireAdoptDenied(t, alice.Update(context.Background(), adopted), "user update")
}

func TestAdminCanAdoptAndNonAdminKeepsEditing(t *testing.T) {
	_, alice, root := databaseAdoptWorld(t)
	annotated := map[string]string{v1alpha1.AnnotationAdopt: "true"}
	user := adoptingUser("u-root", annotated)
	mustCreate(t, root, user, adoptingSchema("s-root", annotated))
	user.Spec.Grants = []v1alpha1.DatabaseGrant{{Database: "app", Level: "readonly"}}
	if err := alice.Update(context.Background(), user); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}
