package tenancy

import (
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
)

func TestProjectRolesCannotReachTheRegistryNamespace(t *testing.T) {
	c, _ := startEnv(t)
	applyFile(t, c, manifestPath("86-tenancy", "10-project-roles.yaml"))
	mustCreate(t, c, namespaceNamed("acme-shop", nil), namespaceNamed("bedrock-registry", nil))
	project := shopProject(member("User", "vera", "viewer"), member("User", "eddie", "editor"), member("User", "olga", "owner"))
	for _, binding := range DesiredRoleBindings(project, nil) {
		mustCreate(t, c, &binding)
	}
	targets := []authorizationv1.ResourceAttributes{
		{Group: "", Resource: "secrets", Namespace: "bedrock-registry"},
		{Group: "", Resource: "secrets", Namespace: "bedrock-registry", Name: "harbor-admin"},
		{Group: "", Resource: "pods", Namespace: "bedrock-registry"},
		{Group: "", Resource: "pods", Subresource: "exec", Namespace: "bedrock-registry"},
		{Group: "", Resource: "configmaps", Namespace: "bedrock-registry"},
		{Group: "", Resource: "services", Namespace: "bedrock-registry"},
		{Group: "apps", Resource: "deployments", Namespace: "bedrock-registry"},
		{Group: "postgresql.cnpg.io", Resource: "clusters", Namespace: "bedrock-registry"},
		{Group: "cert-manager.io", Resource: "certificates", Namespace: "bedrock-registry"},
		{Group: "traefik.io", Resource: "ingressroutes", Namespace: "bedrock-registry"},
		{Group: "traefik.io", Resource: "middlewares", Namespace: "bedrock-registry"},
	}
	for _, user := range []string{"bedrock:vera", "bedrock:eddie", "bedrock:olga"} {
		for _, target := range targets {
			for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
				attributes := target
				attributes.Verb = verb
				if allowed(t, c, user, attributes) {
					t.Errorf("%s may %s %s/%s in the registry namespace", user, verb, target.Group, target.Resource)
				}
			}
		}
	}
}

func TestProjectRolesCoverBothRegistryKinds(t *testing.T) {
	c, _ := startEnv(t)
	applyFile(t, c, manifestPath("86-tenancy", "10-project-roles.yaml"))
	mustCreate(t, c, namespaceNamed("acme-shop", nil))
	project := shopProject(member("User", "vera", "viewer"), member("User", "eddie", "editor"), member("User", "olga", "owner"))
	for _, binding := range DesiredRoleBindings(project, nil) {
		mustCreate(t, c, &binding)
	}
	const bedrock = "bedrock.cloudyfolks.io"
	for _, resource := range []string{"registries", "registrycredentials"} {
		read := authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Verb: "get", Namespace: "acme-shop"}
		write := authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Verb: "create", Namespace: "acme-shop"}
		status := authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Subresource: "status", Verb: "update", Namespace: "acme-shop"}
		for user, want := range map[string][2]bool{"bedrock:vera": {true, false}, "bedrock:eddie": {true, true}, "bedrock:olga": {true, true}} {
			if allowed(t, c, user, read) != want[0] || allowed(t, c, user, write) != want[1] {
				t.Errorf("%s on %s: want read %v write %v", user, resource, want[0], want[1])
			}
			if allowed(t, c, user, status) {
				t.Errorf("%s may write the status of %s", user, resource)
			}
		}
	}
}

func TestTheLoginSecretIsReadableInTheProjectNamespaceOnly(t *testing.T) {
	c, _ := startEnv(t)
	applyFile(t, c, manifestPath("86-tenancy", "10-project-roles.yaml"))
	mustCreate(t, c, namespaceNamed("acme-shop", nil), namespaceNamed("acme-other", nil), namespaceNamed("bedrock-registry", nil))
	project := shopProject(member("User", "vera", "viewer"), member("User", "eddie", "editor"), member("User", "olga", "owner"))
	for _, binding := range DesiredRoleBindings(project, nil) {
		mustCreate(t, c, &binding)
	}
	login := func(namespace string) authorizationv1.ResourceAttributes {
		return authorizationv1.ResourceAttributes{Resource: "secrets", Verb: "get", Namespace: namespace, Name: "ci-docker"}
	}
	for user, want := range map[string]bool{"bedrock:vera": false, "bedrock:eddie": true, "bedrock:olga": true} {
		if got := allowed(t, c, user, login("acme-shop")); got != want {
			t.Errorf("%s reads the login Secret in the project: %v, want %v", user, got, want)
		}
		for _, namespace := range []string{"acme-other", "bedrock-registry"} {
			if allowed(t, c, user, login(namespace)) {
				t.Errorf("%s reads a login Secret in %s", user, namespace)
			}
		}
	}
}
