package tenancy

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const databaseAdoptMessage = "only an administrator may set the adopt annotation of a database user or schema"

func adoptingDatabaseUser(name string, annotations map[string]string) *v1alpha1.DatabaseUser {
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
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "database-access-writer"},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"databaseusers", "databaseschemas", "securitygroups"}, Verbs: []string{"create", "update", "get", "delete"}},
				{APIGroups: []string{""}, Resources: []string{"pods", "configmaps"}, Verbs: []string{"create", "update", "get", "delete"}},
				{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"create"}},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "database-access-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "database-access-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
		namespaceNamed("t-acme", map[string]string{v1alpha1.LabelTenant: "acme"}),
	)
	applyFile(t, admin, manifestPath("86-tenancy", "90-database-adopt-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	probe := adoptingDatabaseUser("probe", map[string]string{v1alpha1.AnnotationAdopt: "true"})
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
	requireAdoptDenied(t, alice.Create(ctx, adoptingDatabaseUser("u-create", annotated)), "user create")
	requireAdoptDenied(t, alice.Create(ctx, adoptingSchema("s-create", annotated)), "schema create")
	user, schema := adoptingDatabaseUser("u-plain", nil), adoptingSchema("s-plain", nil)
	mustCreate(t, alice, user, schema)
	user.Annotations, schema.Annotations = annotated, annotated
	requireAdoptDenied(t, alice.Update(ctx, user), "user update")
	requireAdoptDenied(t, alice.Update(ctx, schema), "schema update")
}

func TestNonAdminCannotRemoveTheAdoptAnnotation(t *testing.T) {
	admin, alice, _ := databaseAdoptWorld(t)
	adopted := adoptingDatabaseUser("u-adopted", map[string]string{v1alpha1.AnnotationAdopt: "true"})
	mustCreate(t, admin, adopted)
	adopted.Annotations = nil
	requireAdoptDenied(t, alice.Update(context.Background(), adopted), "user update")
}

func TestAdminCanAdoptAndNonAdminKeepsEditing(t *testing.T) {
	_, alice, root := databaseAdoptWorld(t)
	annotated := map[string]string{v1alpha1.AnnotationAdopt: "true"}
	user := adoptingDatabaseUser("u-root", annotated)
	mustCreate(t, root, user, adoptingSchema("s-root", annotated))
	user.Spec.Grants = []v1alpha1.DatabaseGrant{{Database: "app", Level: "readonly"}}
	if err := alice.Update(context.Background(), user); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}

func TestNonAdminCannotChangeTheDatabaseSecurityGroup(t *testing.T) {
	admin, alice, root := databaseAdoptWorld(t)
	ctx := context.Background()
	group := &v1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{
		Name: "database-100", Namespace: "t-acme",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: v1alpha1.GroupVersion.String(), Kind: "Database", Name: "orders", UID: "uid-db", Controller: ptr.To(true),
		}},
	}}
	mustCreate(t, admin, group)
	group.Spec.Ingress = []v1alpha1.SecurityGroupRule{{CIDR: "0.0.0.0/0", Port: 5432, Protocol: "tcp"}}
	if err := alice.Update(ctx, group); !apierrors.IsForbidden(err) {
		t.Fatalf("the tenant could change database ingress: %v", err)
	}
	if err := alice.Delete(ctx, group); !apierrors.IsForbidden(err) {
		t.Fatalf("the tenant could delete the database security group: %v", err)
	}
	if err := root.Update(ctx, group); err != nil {
		t.Fatalf("the admin could not change the database security group: %v", err)
	}
	ordinary := &v1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{Name: "customer-rules", Namespace: "t-acme"}}
	mustCreate(t, alice, ordinary)
	if err := alice.Delete(ctx, ordinary); err != nil {
		t.Fatalf("the tenant could not delete an independent security group: %v", err)
	}
}

func TestNonAdminCannotUseTheManagedDatabaseProxyIdentity(t *testing.T) {
	admin, alice, _ := databaseAdoptWorld(t)
	ctx := context.Background()
	const label = "bedrock.cloudyfolks.io/database-gate-client"
	const identity = "approved-database"
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "test:1"}}}
	forged := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "forged", Namespace: "t-acme", Labels: map[string]string{label: identity}},
		Spec:       podSpec,
	}
	if err := eventuallyForbidden(t, denyCreate(admin, alice, forged)); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "database proxy resource or identity") {
		t.Fatalf("the tenant could claim the gate identity: %v", err)
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-app", Namespace: "t-acme"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "customer"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "customer", label: identity}}, Spec: podSpec},
		},
	}
	if err := alice.Create(ctx, deployment); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "database proxy identity on a pod template") {
		t.Fatalf("the tenant could copy the gate identity to a workload template: %v", err)
	}
	settings := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-db-proxy-test", Namespace: "t-acme"}}
	mustCreate(t, admin, settings)
	if err := alice.Delete(ctx, settings); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "database proxy resource or identity") {
		t.Fatalf("the tenant could replace proxy configuration: %v", err)
	}
	delete(deployment.Spec.Template.Labels, label)
	mustCreate(t, alice, deployment)
}
