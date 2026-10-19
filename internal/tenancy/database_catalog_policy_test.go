package tenancy

import (
	"context"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const catalogImage = "ghcr.io/cloudnative-pg/postgresql:17.9-minimal-trixie@sha256:3c9f2df53e15f3fff0aee630beffe73936590333b7fd80f2239bf59b22a9852e"

func catalogVersion(name string) *v1alpha1.DatabaseVersion {
	return &v1alpha1.DatabaseVersion{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.DatabaseVersionSpec{Engine: "postgresql", Major: "17", Image: catalogImage, Port: 5432, Enabled: true},
	}
}

func catalogClass(name string) *v1alpha1.DatabaseClass {
	return &v1alpha1.DatabaseClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.DatabaseClassSpec{
			CPU: resource.MustParse("500m"), Memory: resource.MustParse("1Gi"),
			MinStorageGi: 5, MaxStorageGi: 50, Enabled: true,
		},
	}
}

func catalogWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	return writerWorld(t, []string{"databaseversions", "databaseclasses"}, "90-database-catalog-policy.yaml", catalogClass("probe"))
}

func TestNonAdminCannotWriteTheDatabaseCatalog(t *testing.T) {
	_, alice, _ := catalogWorld(t)
	ctx := context.Background()
	for _, obj := range []client.Object{catalogVersion("postgresql-17"), catalogClass("small")} {
		err := alice.Create(ctx, obj)
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "only an administrator may manage a DatabaseVersion or a DatabaseClass") {
			t.Fatalf("%T create err = %v", obj, err)
		}
	}
}

func TestNonAdminCannotChangeOrDeleteTheDatabaseCatalog(t *testing.T) {
	admin, alice, _ := catalogWorld(t)
	ctx := context.Background()
	class := catalogClass("small")
	version := catalogVersion("postgresql-17")
	mustCreate(t, admin, class, version)
	class.Spec.Enabled = false
	if err := alice.Update(ctx, class); !apierrors.IsForbidden(err) {
		t.Fatalf("update err = %v", err)
	}
	if err := alice.Delete(ctx, version); !apierrors.IsForbidden(err) {
		t.Fatalf("delete err = %v", err)
	}
}

func TestAdminCanManageTheDatabaseCatalog(t *testing.T) {
	_, _, root := catalogWorld(t)
	ctx := context.Background()
	class := catalogClass("small")
	version := catalogVersion("postgresql-17")
	mustCreate(t, root, class, version)
	class.Spec.Enabled = false
	if err := root.Update(ctx, class); err != nil {
		t.Fatalf("update err = %v", err)
	}
	if err := root.Delete(ctx, version); err != nil {
		t.Fatalf("delete err = %v", err)
	}
}

func TestEveryAuthenticatedUserCanReadTheDatabaseCatalog(t *testing.T) {
	c, _ := startEnv(t)
	applyFile(t, c, manifestPath("86-tenancy", "90-database-catalog-policy.yaml"))
	const bedrock = "bedrock.cloudyfolks.io"
	for _, resource := range []string{"databaseversions", "databaseclasses"} {
		for _, verb := range []string{"get", "list", "watch"} {
			review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
				User:               "bedrock:vera",
				Groups:             []string{"system:authenticated"},
				ResourceAttributes: &authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Verb: verb},
			}}
			if err := c.Create(context.Background(), review); err != nil {
				t.Fatal(err)
			}
			if !review.Status.Allowed {
				t.Errorf("%s %s denied", verb, resource)
			}
		}
		for _, verb := range []string{"create", "update", "delete"} {
			review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
				User:               "bedrock:vera",
				Groups:             []string{"system:authenticated"},
				ResourceAttributes: &authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Verb: verb},
			}}
			if err := c.Create(context.Background(), review); err != nil {
				t.Fatal(err)
			}
			if review.Status.Allowed {
				t.Errorf("%s %s allowed", verb, resource)
			}
		}
	}
}
