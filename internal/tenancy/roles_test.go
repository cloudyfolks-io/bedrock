package tenancy

import (
	"context"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func allowed(t *testing.T, c client.Client, user string, attributes authorizationv1.ResourceAttributes) bool {
	t.Helper()
	review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{User: user, ResourceAttributes: &attributes}}
	if err := c.Create(context.Background(), review); err != nil {
		t.Fatal(err)
	}
	return review.Status.Allowed
}

func TestProjectRolesWorkThroughRoleBindings(t *testing.T) {
	c, _ := startEnv(t)
	applyFile(t, c, manifestPath("86-tenancy", "10-project-roles.yaml"))
	mustCreate(t, c, namespaceNamed("acme-shop", nil), namespaceNamed("acme-other", nil))
	project := shopProject(member("User", "vera", "viewer"), member("User", "eddie", "editor"), member("User", "olga", "owner"))
	for _, binding := range DesiredRoleBindings(project, nil) {
		mustCreate(t, c, &binding)
	}
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
