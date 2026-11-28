package tenancy

import (
	"context"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func allowed(t *testing.T, c client.Client, user string, attributes authorizationv1.ResourceAttributes) bool {
	t.Helper()
	ok, err := reviewAccess(t.Context(), c, user, attributes)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func reviewAccess(ctx context.Context, c client.Client, user string, attributes authorizationv1.ResourceAttributes) (bool, error) {
	review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{User: user, ResourceAttributes: &attributes}}
	if err := c.Create(ctx, review); err != nil {
		return false, err
	}
	return review.Status.Allowed, nil
}

func projectRoleEnv(t *testing.T, otherNamespaces ...string) client.Client {
	t.Helper()
	c, _ := startEnv(t)
	applyFile(t, c, manifestPath("86-tenancy", "10-project-roles.yaml"))
	const namespace = "acme-shop"
	for _, name := range append([]string{namespace}, otherNamespaces...) {
		mustCreate(t, c, namespaceNamed(name, nil))
	}
	project := shopProject(member("User", "vera", "viewer"), member("User", "eddie", "editor"), member("User", "olga", "owner"))
	for _, binding := range DesiredRoleBindings(project, nil) {
		mustCreate(t, c, &binding)
	}
	// Persisted bindings can lag in the authorizer's cache. Require a grant for each role before testing denials.
	for _, check := range []struct{ user, verb string }{
		{"bedrock:vera", "get"},
		{"bedrock:eddie", "create"},
		{"bedrock:olga", "deletecollection"},
	} {
		attributes := authorizationv1.ResourceAttributes{
			Group: "bedrock.cloudyfolks.io", Resource: "databases", Verb: check.verb, Namespace: namespace,
		}
		err := wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, 15*time.Second, true, func(ctx context.Context) (bool, error) {
			return reviewAccess(ctx, c, check.user, attributes)
		})
		if err != nil {
			t.Fatalf("wait for %s to %s databases: %v", check.user, check.verb, err)
		}
	}
	return c
}

func TestProjectRolesWorkThroughRoleBindings(t *testing.T) {
	c := projectRoleEnv(t, "acme-other")
	const bedrock = "bedrock.cloudyfolks.io"
	in := func(group, resource, subresource, verb, namespace string) authorizationv1.ResourceAttributes {
		return authorizationv1.ResourceAttributes{Group: group, Resource: resource, Subresource: subresource, Verb: verb, Namespace: namespace}
	}
	cases := []struct {
		user       string
		attributes authorizationv1.ResourceAttributes
		want       bool
	}{
		{"bedrock:vera", in("", "pods", "", "get", "acme-shop"), true},
		{"bedrock:vera", in("", "secrets", "", "get", "acme-shop"), false},
		{"bedrock:eddie", in("apps", "deployments", "", "create", "acme-shop"), true},
		{"bedrock:eddie", in("kubevirt.io", "virtualmachines", "", "create", "acme-shop"), false},
		{"bedrock:eddie", in(bedrock, "virtualmachines", "", "create", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "virtualmachines", "start", "update", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "virtualmachines", "console/vnc", "get", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "images", "upload", "create", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "virtualmachines", "migrate", "update", "acme-shop"), false},
		{"bedrock:vera", in(bedrock, "containers", "", "get", "acme-shop"), true},
		{"bedrock:vera", in(bedrock, "containers", "", "create", "acme-shop"), false},
		{"bedrock:vera", in(bedrock, "containers", "logs", "get", "acme-shop"), true},
		{"bedrock:vera", in(bedrock, "containers", "exec", "get", "acme-shop"), false},
		{"bedrock:vera", in(bedrock, "containers", "restart", "update", "acme-shop"), false},
		{"bedrock:eddie", in(bedrock, "containers", "", "create", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "containers", "", "delete", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "containers", "logs", "get", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "containers", "exec", "get", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "containers", "restart", "update", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "containers", "exec", "get", "acme-other"), false},
		{"bedrock:olga", in(bedrock, "containers", "", "deletecollection", "acme-shop"), true},
		{"bedrock:olga", in(bedrock, "containers", "exec", "get", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "containers", "", "deletecollection", "acme-shop"), false},
		{"bedrock:vera", in(bedrock, "builds", "", "get", "acme-shop"), true},
		{"bedrock:vera", in(bedrock, "builds", "", "create", "acme-shop"), false},
		{"bedrock:vera", in(bedrock, "builds", "logs", "get", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "builds", "", "create", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "builds", "", "delete", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "builds", "logs", "get", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "builds", "logs", "get", "acme-other"), false},
		{"bedrock:eddie", in(bedrock, "builds", "", "deletecollection", "acme-shop"), false},
		{"bedrock:olga", in(bedrock, "builds", "", "create", "acme-shop"), true},
		{"bedrock:olga", in(bedrock, "builds", "", "deletecollection", "acme-shop"), true},
		{"bedrock:olga", in(bedrock, "builds", "logs", "get", "acme-shop"), true},
		{"bedrock:vera", in(bedrock, "kubernetesclusters", "", "get", "acme-shop"), true},
		{"bedrock:vera", in(bedrock, "kubernetesclusters", "", "list", "acme-shop"), true},
		{"bedrock:vera", in(bedrock, "kubernetesclusters", "", "create", "acme-shop"), false},
		{"bedrock:eddie", in(bedrock, "kubernetesclusters", "", "create", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "kubernetesclusters", "", "patch", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "kubernetesclusters", "", "delete", "acme-shop"), true},
		{"bedrock:eddie", in(bedrock, "kubernetesclusters", "", "deletecollection", "acme-shop"), false},
		{"bedrock:eddie", in(bedrock, "kubernetesclusters", "", "create", "acme-other"), false},
		{"bedrock:olga", in(bedrock, "kubernetesclusters", "", "deletecollection", "acme-shop"), true},
		{"bedrock:vera", in("", "secrets", "", "get", "acme-shop"), false},
		{"bedrock:olga", in("", "pods", "", "deletecollection", "acme-shop"), true},
		{"bedrock:olga", in("networking.k8s.io", "networkpolicies", "", "create", "acme-shop"), true},
		{"bedrock:vera", in("", "pods", "", "get", "acme-other"), false},
		{"bedrock:eddie", in("", "pods", "", "get", "acme-other"), false},
		{"bedrock:olga", in("", "pods", "", "get", "acme-other"), false},
		{"bedrock:vera", in(bedrock, "tenants", "", "get", ""), false},
		{"bedrock:olga", in(bedrock, "tenants", "", "get", ""), false},
	}
	for _, c2 := range cases {
		if got := allowed(t, c, c2.user, c2.attributes); got != c2.want {
			t.Errorf("%s %s %s/%s %s in %q: got %v, want %v", c2.user, c2.attributes.Verb, c2.attributes.Group, c2.attributes.Resource, c2.attributes.Subresource, c2.attributes.Namespace, got, c2.want)
		}
	}
}
