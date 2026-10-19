package tenancy

import (
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
)

func TestProjectRolesCoverTheDatabaseKinds(t *testing.T) {
	c, _ := startEnv(t)
	applyFile(t, c, manifestPath("86-tenancy", "10-project-roles.yaml"))
	mustCreate(t, c, namespaceNamed("acme-shop", nil))
	project := shopProject(member("User", "vera", "viewer"), member("User", "eddie", "editor"), member("User", "olga", "owner"))
	for _, binding := range DesiredRoleBindings(project, nil) {
		mustCreate(t, c, &binding)
	}
	const bedrock = "bedrock.cloudyfolks.io"
	for _, resource := range []string{"databases", "databaseusers", "databaseschemas"} {
		for _, verb := range []string{"get", "list", "watch"} {
			attributes := authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Verb: verb, Namespace: "acme-shop"}
			for _, user := range []string{"bedrock:vera", "bedrock:eddie", "bedrock:olga"} {
				if !allowed(t, c, user, attributes) {
					t.Errorf("%s may not %s %s", user, verb, resource)
				}
			}
		}
		for _, verb := range []string{"create", "update", "patch", "delete"} {
			attributes := authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Verb: verb, Namespace: "acme-shop"}
			for user, want := range map[string]bool{"bedrock:vera": false, "bedrock:eddie": true, "bedrock:olga": true} {
				if allowed(t, c, user, attributes) != want {
					t.Errorf("%s %s %s: want %v", user, verb, resource, want)
				}
			}
		}
	}
	for _, resource := range []string{"databaseversions", "databaseclasses"} {
		for _, verb := range []string{"create", "update", "patch", "delete"} {
			attributes := authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Verb: verb}
			for _, user := range []string{"bedrock:vera", "bedrock:eddie", "bedrock:olga"} {
				if allowed(t, c, user, attributes) {
					t.Errorf("%s may %s %s", user, verb, resource)
				}
			}
		}
	}
}

func TestNoProjectRoleHasAVerbOnTheDatabasePlatformNamespace(t *testing.T) {
	c, _ := startEnv(t)
	applyFile(t, c, manifestPath("86-tenancy", "10-project-roles.yaml"))
	mustCreate(t, c, namespaceNamed("acme-shop", nil), namespaceNamed("bedrock-dbaas", nil))
	project := shopProject(member("User", "vera", "viewer"), member("User", "eddie", "editor"), member("User", "olga", "owner"))
	for _, binding := range DesiredRoleBindings(project, nil) {
		mustCreate(t, c, &binding)
	}
	targets := []authorizationv1.ResourceAttributes{
		{Group: "", Resource: "pods", Namespace: "bedrock-dbaas"},
		{Group: "", Resource: "pods", Subresource: "exec", Namespace: "bedrock-dbaas"},
		{Group: "", Resource: "pods", Subresource: "log", Namespace: "bedrock-dbaas"},
		{Group: "", Resource: "secrets", Namespace: "bedrock-dbaas"},
		{Group: "", Resource: "services", Namespace: "bedrock-dbaas"},
		{Group: "", Resource: "configmaps", Namespace: "bedrock-dbaas"},
		{Group: "apps", Resource: "deployments", Namespace: "bedrock-dbaas"},
		{Group: "postgresql.cnpg.io", Resource: "clusters", Namespace: "bedrock-dbaas"},
		{Group: "postgresql.cnpg.io", Resource: "backups", Namespace: "bedrock-dbaas"},
		{Group: "barmancloud.cnpg.io", Resource: "objectstores", Namespace: "bedrock-dbaas"},
		{Group: "cert-manager.io", Resource: "certificates", Namespace: "bedrock-dbaas"},
		{Group: "cert-manager.io", Resource: "certificates", Namespace: "acme-shop"},
		{Group: "cert-manager.io", Resource: "issuers", Namespace: "acme-shop"},
	}
	for _, user := range []string{"bedrock:vera", "bedrock:eddie", "bedrock:olga"} {
		for _, target := range targets {
			for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
				attributes := target
				attributes.Verb = verb
				if allowed(t, c, user, attributes) {
					t.Errorf("%s may %s %s/%s/%s in %s", user, verb, target.Group, target.Resource, target.Subresource, target.Namespace)
				}
			}
		}
	}
}
