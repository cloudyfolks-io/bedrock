package release

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var platformPlurals = []string{"apitokens", "authcodes", "authrequests", "credentials", "devicerequests", "groups", "identityproviders", "oauthclients", "refreshtokens", "sessions", "signingkeys", "users"}

type roleRule struct {
	groups, resources, names, verbs []string
}

func tenancyManifest(t *testing.T, file string) []*unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "86-tenancy", file))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := splitDocuments(raw)
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

func projectRoles(t *testing.T) map[string][]roleRule {
	t.Helper()
	roles := map[string][]roleRule{}
	for _, obj := range tenancyManifest(t, "10-project-roles.yaml") {
		rules, _, err := unstructured.NestedSlice(obj.Object, "rules")
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range rules {
			entry, _ := raw.(map[string]any)
			roles[obj.GetName()] = append(roles[obj.GetName()], roleRule{
				groups:    stringsOf(entry["apiGroups"]),
				resources: stringsOf(entry["resources"]),
				names:     stringsOf(entry["resourceNames"]),
				verbs:     stringsOf(entry["verbs"]),
			})
		}
	}
	return roles
}

func stringsOf(raw any) []string {
	items, _ := raw.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(string))
	}
	return out
}

func roleAllows(rules []roleRule, group, resource, verb string) bool {
	return slices.ContainsFunc(rules, func(rule roleRule) bool {
		return slices.Contains(rule.groups, group) && slices.Contains(rule.resources, resource) && slices.Contains(rule.verbs, verb)
	})
}

func TestProjectRolesGrantTheMatrix(t *testing.T) {
	const bedrock = "bedrock.cloudyfolks.io"
	cases := []struct {
		role, group, resource, verb string
		want                        bool
	}{
		{"viewer", bedrock, "virtualmachines", "get", true},
		{"viewer", bedrock, "virtualmachines", "create", false},
		{"viewer", "", "secrets", "get", false},
		{"viewer", "", "pods/log", "get", true},
		{"viewer", bedrock, "virtualmachines/start", "update", false},
		{"viewer", bedrock, "virtualmachines/stop", "update", false},
		{"viewer", bedrock, "virtualmachines/restart", "update", false},
		{"viewer", bedrock, "virtualmachines/snapshot", "update", false},
		{"viewer", bedrock, "virtualmachines/snapshot", "get", false},
		{"viewer", bedrock, "virtualmachines/console/vnc", "get", false},
		{"viewer", bedrock, "virtualmachines/console/serial", "get", false},
		{"viewer", bedrock, "virtualmachines/migrate", "update", false},
		{"viewer", bedrock, "virtualmachines/migrate", "get", false},
		{"viewer", bedrock, "images/upload", "create", false},
		{"viewer", bedrock, "images/upload", "get", false},
		{"editor", bedrock, "virtualmachines/start", "update", true},
		{"editor", bedrock, "virtualmachines/stop", "update", true},
		{"editor", bedrock, "virtualmachines/restart", "update", true},
		{"editor", bedrock, "virtualmachines/snapshot", "update", true},
		{"editor", bedrock, "virtualmachines/snapshot", "get", true},
		{"editor", bedrock, "virtualmachines/console/vnc", "get", true},
		{"editor", bedrock, "virtualmachines/console/serial", "get", true},
		{"editor", bedrock, "virtualmachines/migrate", "update", false},
		{"editor", bedrock, "virtualmachines/migrate", "get", false},
		{"editor", bedrock, "images/upload", "create", true},
		{"editor", bedrock, "images/upload", "get", false},
		{"owner", bedrock, "virtualmachines/start", "update", true},
		{"owner", bedrock, "virtualmachines/stop", "update", true},
		{"owner", bedrock, "virtualmachines/restart", "update", true},
		{"owner", bedrock, "virtualmachines/snapshot", "update", true},
		{"owner", bedrock, "virtualmachines/snapshot", "get", true},
		{"owner", bedrock, "virtualmachines/console/vnc", "get", true},
		{"owner", bedrock, "virtualmachines/console/serial", "get", true},
		{"owner", bedrock, "virtualmachines/migrate", "update", false},
		{"owner", bedrock, "virtualmachines/migrate", "get", false},
		{"owner", bedrock, "images/upload", "create", true},
		{"owner", bedrock, "images/upload", "get", false},
		{"viewer", bedrock, "virtualmachines/start", "update", false},
		{"viewer", bedrock, "virtualmachines/console/vnc", "get", false},
		{"viewer", bedrock, "images/upload", "create", false},
		{"editor", "apps", "deployments", "create", true},
		{"editor", "", "secrets", "get", true},
		{"editor", "", "secrets", "create", true},
		{"editor", "", "pods/exec", "create", true},
		{"editor", bedrock, "virtualmachines/start", "update", true},
		{"editor", bedrock, "images/upload", "create", true},
		{"editor", bedrock, "virtualmachines/console/vnc", "get", true},
		{"editor", bedrock, "virtualmachines/console/serial", "get", true},
		{"editor", bedrock, "virtualmachines/snapshot", "get", true},
		{"editor", bedrock, "virtualmachines/console/vnc", "update", false},
		{"editor", bedrock, "virtualmachines/migrate", "update", false},
		{"owner", bedrock, "virtualmachines/migrate", "update", false},
		{"editor", "", "pods", "deletecollection", false},
		{"editor", "networking.k8s.io", "networkpolicies", "create", false},
		{"owner", "", "pods", "deletecollection", true},
		{"owner", "networking.k8s.io", "networkpolicies", "create", true},
		{"owner", bedrock, "volumes", "delete", true},
	}
	roles := projectRoles(t)
	for _, c := range cases {
		if got := roleAllows(roles["bedrock-project-"+c.role], c.group, c.resource, c.verb); got != c.want {
			t.Errorf("%s %s %s/%s: got %v, want %v", c.role, c.verb, c.group, c.resource, got, c.want)
		}
	}
}

func TestProjectRolesUseOnlyTheAllowedGroups(t *testing.T) {
	allowed := []string{"", "apps", "networking.k8s.io", "aaq.kubevirt.io", "bedrock.cloudyfolks.io"}
	for name, rules := range projectRoles(t) {
		for _, rule := range rules {
			if len(rule.names) != 0 {
				t.Errorf("%s: rule with resourceNames %v", name, rule.names)
			}
			for _, group := range rule.groups {
				if !slices.Contains(allowed, group) {
					t.Errorf("%s: group %q is not allowed", name, group)
				}
			}
			for _, entry := range slices.Concat(rule.groups, rule.resources, rule.verbs) {
				if entry == "*" {
					t.Errorf("%s: wildcard in %+v", name, rule)
				}
			}
		}
	}
}

func TestProjectRolesGrantNothingOnVirtualizationGroups(t *testing.T) {
	denied := []string{"kubevirt.io", "cdi.kubevirt.io", "snapshot.kubevirt.io", "kubeovn.io", "ceph.rook.io", "objectbucket.io", "velero.io", "k8s.cni.cncf.io"}
	for name, rules := range projectRoles(t) {
		for _, rule := range rules {
			for _, group := range rule.groups {
				if slices.Contains(denied, group) {
					t.Errorf("%s: group %q is denied", name, group)
				}
			}
		}
	}
}

func namespacedBedrockPlurals(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "manifests", "00-crds", "bedrock.cloudyfolks.io_*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no CRD files: %v", err)
	}
	plurals := []string{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		objects, err := splitDocuments(raw)
		if err != nil || len(objects) != 1 {
			t.Fatalf("%s: %v", file, err)
		}
		scope, _, _ := unstructured.NestedString(objects[0].Object, "spec", "scope")
		plural, _, _ := unstructured.NestedString(objects[0].Object, "spec", "names", "plural")
		if scope == "Namespaced" {
			plurals = append(plurals, plural)
		}
	}
	return plurals
}

func TestProjectRolesCoverEveryTenantKind(t *testing.T) {
	const bedrock = "bedrock.cloudyfolks.io"
	roles := projectRoles(t)
	for _, plural := range namespacedBedrockPlurals(t) {
		if slices.Contains(platformPlurals, plural) {
			continue
		}
		wanted := []struct{ role, verb string }{
			{"viewer", "get"}, {"viewer", "list"}, {"viewer", "watch"},
			{"editor", "create"}, {"editor", "update"}, {"editor", "patch"}, {"editor", "delete"},
			{"owner", "deletecollection"},
		}
		for _, w := range wanted {
			if !roleAllows(roles["bedrock-project-"+w.role], bedrock, plural, w.verb) {
				t.Errorf("%s lacks %s on %s: add %s to the project roles or to platformPlurals", w.role, w.verb, plural, plural)
			}
		}
	}
}

func TestProjectRolesGrantNothingOnPlatformKinds(t *testing.T) {
	forbidden := slices.Concat(platformPlurals, []string{"roles", "rolebindings", "serviceaccounts", "nodes", "namespaces", "tenants", "projects", "networks"})
	for name, rules := range projectRoles(t) {
		for _, rule := range rules {
			for _, resource := range rule.resources {
				if slices.Contains(forbidden, strings.SplitN(resource, "/", 2)[0]) {
					t.Errorf("%s: resource %q is a platform kind", name, resource)
				}
			}
		}
	}
}

func TestProjectRolesAreLabelled(t *testing.T) {
	objects := tenancyManifest(t, "10-project-roles.yaml")
	if len(objects) != 3 {
		t.Fatalf("objects = %d", len(objects))
	}
	for _, obj := range objects {
		labels := obj.GetLabels()
		if labels["bedrock.cloudyfolks.io/kind"] != "ClusterRole" || labels["bedrock.cloudyfolks.io/name"] != obj.GetName() {
			t.Errorf("%s: labels %v", obj.GetName(), labels)
		}
	}
}

func TestTenancyComponentIsConfigured(t *testing.T) {
	cfg, err := LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range cfg.Components {
		if component.Name == "tenancy" {
			if component.Order != 86 || !slices.Equal(component.Dirs, []string{"manifests/86-tenancy"}) {
				t.Fatalf("component = %+v", component)
			}
			return
		}
	}
	t.Fatal("component tenancy missing")
}

func TestComponentOrdersAreUnique(t *testing.T) {
	cfg, err := LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]string{}
	for _, component := range cfg.Components {
		if component.Addon {
			continue
		}
		if other, found := seen[component.Order]; found {
			t.Errorf("components %s and %s share order %d", other, component.Name, component.Order)
		}
		seen[component.Order] = component.Name
	}
}
