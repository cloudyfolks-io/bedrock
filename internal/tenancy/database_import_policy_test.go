package tenancy

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOnlyAnAdministratorCanChangeTheDatabaseImportMarker(t *testing.T) {
	admin, cfg := startEnv(t)
	ctx := context.Background()
	const rbacName = "database-import-writer"
	const tenant = "t-acme"
	const username = "system:serviceaccount:t-acme:database-api"
	mustCreate(t, admin,
		namespaceNamed(tenant, map[string]string{v1alpha1.LabelTenant: "acme"}),
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: rbacName}, Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"databases"}, Verbs: []string{"create", "get", "update"},
		}}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: rbacName},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: rbacName},
			Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: username}},
		},
	)
	applyFile(t, admin, manifestPath("86-tenancy", "91-database-import-policy.yaml"))
	actor := actingAs(t, cfg, username, "system:authenticated")
	database := &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: tenant, Annotations: map[string]string{
			v1alpha1.DatabaseImportMarker: "true", v1alpha1.DatabaseImportSource: "databases/orders",
		}},
		Spec: v1alpha1.DatabaseSpec{Engine: "postgresql", Version: "17", Class: "micro", StorageGi: 20},
	}
	if err := eventuallyForbidden(t, denyCreate(admin, actor, database)); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "database import marker") {
		t.Fatalf("a database service account could set the import marker: %v", err)
	}
	ordinary := database.DeepCopy()
	ordinary.Name = "ordinary"
	delete(ordinary.Annotations, v1alpha1.DatabaseImportMarker)
	mustCreate(t, actor, ordinary)
	mustCreate(t, admin, database)
	delete(database.Annotations, v1alpha1.DatabaseImportMarker)
	if err := actor.Update(ctx, database); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "database import marker") {
		t.Fatalf("a database service account could remove the import marker: %v", err)
	}
}
