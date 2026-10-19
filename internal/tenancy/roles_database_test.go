package tenancy

import (
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
)

func TestProjectRolesGrantDatabaseLogsToEveryRoleInItsOwnProject(t *testing.T) {
	c, _ := startEnv(t)
	applyFile(t, c, manifestPath("86-tenancy", "10-project-roles.yaml"))
	mustCreate(t, c, namespaceNamed("acme-shop", nil), namespaceNamed("acme-other", nil))
	project := shopProject(member("User", "vera", "viewer"), member("User", "eddie", "editor"), member("User", "olga", "owner"))
	for _, binding := range DesiredRoleBindings(project, nil) {
		mustCreate(t, c, &binding)
	}
	const bedrock = "bedrock.cloudyfolks.io"
	attributes := func(verb, namespace string) authorizationv1.ResourceAttributes {
		return authorizationv1.ResourceAttributes{Group: bedrock, Resource: "databases", Subresource: "logs", Verb: verb, Namespace: namespace}
	}
	cases := []struct {
		user       string
		attributes authorizationv1.ResourceAttributes
		want       bool
	}{
		{"bedrock:vera", attributes("get", "acme-shop"), true},
		{"bedrock:eddie", attributes("get", "acme-shop"), true},
		{"bedrock:olga", attributes("get", "acme-shop"), true},
		{"bedrock:vera", attributes("get", "acme-other"), false},
		{"bedrock:eddie", attributes("get", "acme-other"), false},
		{"bedrock:olga", attributes("get", "acme-other"), false},
		{"bedrock:vera", attributes("update", "acme-shop"), false},
		{"bedrock:olga", attributes("create", "acme-shop"), false},
		{"bedrock:stranger", attributes("get", "acme-shop"), false},
	}
	for _, c2 := range cases {
		if got := allowed(t, c, c2.user, c2.attributes); got != c2.want {
			t.Errorf("%s %s databases/logs in %q: got %v, want %v", c2.user, c2.attributes.Verb, c2.attributes.Namespace, got, c2.want)
		}
	}
}
