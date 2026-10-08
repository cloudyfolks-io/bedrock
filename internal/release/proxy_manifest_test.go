package release

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var authnPlurals = []string{
	"apitokens", "authcodes", "authrequests", "credentials", "devicerequests", "groups",
	"identityproviders", "oauthclients", "refreshtokens", "sessions", "signingkeys", "users",
}

func proxyManifest(t *testing.T, file string) []*unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "88-proxy", file))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := splitDocuments(raw)
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

func allProxyObjects(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	var objects []*unstructured.Unstructured
	for _, file := range []string{"00-namespace.yaml", "10-rbac.yaml", "20-workload.yaml"} {
		objects = append(objects, proxyManifest(t, file)...)
	}
	return objects
}

func findObject(t *testing.T, objects []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, obj := range objects {
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}
	t.Fatalf("no %s %s", kind, name)
	return nil
}

type policyRule struct {
	groups, resources, names, verbs []string
}

func rulesOf(obj *unstructured.Unstructured) []policyRule {
	raw, _, _ := unstructured.NestedSlice(obj.Object, "rules")
	rules := make([]policyRule, 0, len(raw))
	for _, item := range raw {
		rule, _ := item.(map[string]any)
		groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
		resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
		names, _, _ := unstructured.NestedStringSlice(rule, "resourceNames")
		verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
		rules = append(rules, policyRule{groups: groups, resources: resources, names: names, verbs: verbs})
	}
	return rules
}

func allows(rules []policyRule, group, resource, verb string) bool {
	for _, rule := range rules {
		if slices.Contains(rule.groups, group) && slices.Contains(rule.resources, resource) && slices.Contains(rule.verbs, verb) {
			return true
		}
	}
	return false
}

type crdKind struct {
	plural    string
	hasStatus bool
}

func bedrockKinds(t *testing.T) []crdKind {
	t.Helper()
	dir := filepath.Join("..", "..", "manifests", "00-crds")
	files, err := filepath.Glob(filepath.Join(dir, "bedrock.cloudyfolks.io_*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("CRD files: %v, %v", files, err)
	}
	var kinds []crdKind
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		objects, err := splitDocuments(raw)
		if err != nil {
			t.Fatal(err)
		}
		plural, _, _ := unstructured.NestedString(objects[0].Object, "spec", "names", "plural")
		versions, _, _ := unstructured.NestedSlice(objects[0].Object, "spec", "versions")
		hasStatus := false
		for _, item := range versions {
			version, _ := item.(map[string]any)
			_, found, _ := unstructured.NestedMap(version, "subresources", "status")
			hasStatus = hasStatus || found
		}
		kinds = append(kinds, crdKind{plural: plural, hasStatus: hasStatus})
	}
	return kinds
}

func TestProxyDeploymentImageIsRewritten(t *testing.T) {
	image := "ghcr.io/cloudyfolks-io/bedrock:v9.9.9"
	objects := proxyManifest(t, "20-workload.yaml")
	rewritten := rewriteBedrockImage([]rendered{{group: "88-proxy", file: "20-workload.yaml", objects: objects}}, image)
	deployment := findObject(t, rewritten[0].objects, "Deployment", "bedrock-proxy")
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	container, _ := containers[0].(map[string]any)
	wantArgs := []any{"proxy", "serve", "--prometheus-url=http://prometheus-operated.monitoring.svc:9090"}
	if container["image"] != image || !reflect.DeepEqual(container["args"], wantArgs) {
		t.Fatalf("image %v args %v", container["image"], container["args"])
	}
}

func TestProxyWorkloadHardening(t *testing.T) {
	objects := proxyManifest(t, "20-workload.yaml")
	deployment := findObject(t, objects, "Deployment", "bedrock-proxy")
	replicas, _, _ := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	template := []string{"spec", "template", "spec"}
	nonRoot, _, _ := unstructured.NestedBool(deployment.Object, append(template, "securityContext", "runAsNonRoot")...)
	seccomp, _, _ := unstructured.NestedString(deployment.Object, append(template, "securityContext", "seccompProfile", "type")...)
	_, preferred, _ := unstructured.NestedSlice(deployment.Object, append(template, "affinity", "podAntiAffinity", "preferredDuringSchedulingIgnoredDuringExecution")...)
	_, required, _ := unstructured.NestedSlice(deployment.Object, append(template, "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution")...)
	containers, _, _ := unstructured.NestedSlice(deployment.Object, append(template, "containers")...)
	container, _ := containers[0].(map[string]any)
	readOnly, _, _ := unstructured.NestedBool(container, "securityContext", "readOnlyRootFilesystem")
	escalation, found, _ := unstructured.NestedBool(container, "securityContext", "allowPrivilegeEscalation")
	drop, _, _ := unstructured.NestedStringSlice(container, "securityContext", "capabilities", "drop")
	limit, _, _ := unstructured.NestedString(container, "resources", "limits", "memory")
	request, _, _ := unstructured.NestedString(container, "resources", "requests", "memory")
	if replicas != 2 || !nonRoot || seccomp != "RuntimeDefault" || !preferred || required || !readOnly || !found || escalation ||
		!reflect.DeepEqual(drop, []string{"ALL"}) || limit != "512Mi" || request != "128Mi" {
		t.Fatalf("hardening: replicas %d nonRoot %v seccomp %s preferred %v required %v readOnly %v escalation %v drop %v limit %s request %s",
			replicas, nonRoot, seccomp, preferred, required, readOnly, escalation, drop, limit, request)
	}
	budget := findObject(t, objects, "PodDisruptionBudget", "bedrock-proxy")
	if unavailable, _, _ := unstructured.NestedInt64(budget.Object, "spec", "maxUnavailable"); unavailable != 1 {
		t.Fatalf("maxUnavailable %d", unavailable)
	}
	if class, _, _ := unstructured.NestedString(deployment.Object, append(template, "priorityClassName")...); class != "system-cluster-critical" {
		t.Fatalf("priority class %q", class)
	}
}

func TestProxyMountsTLSAndOnlyTheAuthnCA(t *testing.T) {
	deployment := findObject(t, proxyManifest(t, "20-workload.yaml"), "Deployment", "bedrock-proxy")
	volumes, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "volumes")
	byName := map[string]map[string]any{}
	for _, item := range volumes {
		volume, _ := item.(map[string]any)
		byName[volume["name"].(string)] = volume
	}
	authnCA, _, _ := unstructured.NestedMap(byName["authn-ca"], "secret")
	items, _ := authnCA["items"].([]any)
	if authnCA["secretName"] != "bedrock-authn-tls" || len(items) != 1 || !reflect.DeepEqual(items[0], map[string]any{"key": "ca.crt", "path": "ca.crt"}) {
		t.Fatalf("authn-ca volume %v", authnCA)
	}
	if tls, _, _ := unstructured.NestedString(byName["tls"], "secret", "secretName"); tls != "bedrock-proxy-tls" {
		t.Fatalf("tls volume %v", byName["tls"])
	}
}

func TestProxyNamespaceComesFirst(t *testing.T) {
	objects, err := loadObjects(os.DirFS(filepath.Join("..", "..", "manifests")), "88-proxy", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	namespaceAt := -1
	for i, obj := range objects {
		if obj.GetKind() == "Namespace" && obj.GetName() == "bedrock-proxy" {
			namespaceAt = i
		}
	}
	if namespaceAt < 0 {
		t.Fatal("the group holds no Namespace bedrock-proxy")
	}
	for i, obj := range objects {
		if obj.GetNamespace() == "bedrock-proxy" && i < namespaceAt {
			t.Errorf("%s %s comes before its Namespace", obj.GetKind(), obj.GetName())
		}
	}
}

func TestProxyBaseRoleHoldsNoAuthnKindsAndNoBroadWrites(t *testing.T) {
	objects := proxyManifest(t, "10-rbac.yaml")
	base := rulesOf(findObject(t, objects, "ClusterRole", "bedrock-proxy-base"))
	group := "bedrock.cloudyfolks.io"
	for _, kind := range bedrockKinds(t) {
		if !slices.Contains(authnPlurals, kind.plural) {
			continue
		}
		for _, verb := range []string{"get", "list", "watch", "patch", "update", "create", "delete"} {
			if allows(base, group, kind.plural, verb) {
				t.Errorf("the base role allows %s on the authn kind %s", verb, kind.plural)
			}
		}
	}
	for _, kind := range bedrockKinds(t) {
		for _, resource := range []string{kind.plural, kind.plural + "/status"} {
			for _, verb := range []string{"watch", "update", "delete", "deletecollection"} {
				if allows(base, group, resource, verb) {
					t.Errorf("the base role allows %s on %s", verb, resource)
				}
			}
		}
	}
	for _, resource := range []string{"hosts", "hostconfigs", "clusters", "clusterconfigs", "nodeupgrades", "releases", "addons", "tenants"} {
		for _, verb := range []string{"get", "list", "patch"} {
			if allows(base, group, resource, verb) {
				t.Errorf("the base role allows %s on %s", verb, resource)
			}
		}
	}
	if !allows(base, group, "projects", "list") || !allows(base, group, "settings", "get") {
		t.Error("the base role needs projects list and settings get for the proxy itself")
	}
	local := rulesOf(findObject(t, objects, "Role", "bedrock-proxy"))
	for _, plural := range []string{"sessions", "users"} {
		if !allows(local, group, plural, "get") || allows(local, group, plural, "list") {
			t.Errorf("Role bedrock-proxy must allow only get on %s", plural)
		}
	}
	store := rulesOf(findObject(t, objects, "Role", "bedrock-proxy-store"))
	if allows(store, "", "secrets", "patch") || !allows(store, "", "secrets", "update") {
		t.Error("the store role must not patch Secrets and must update them")
	}
}

func TestProxyRolesAreScopedAndHaveNoWildcards(t *testing.T) {
	objects := proxyManifest(t, "10-rbac.yaml")
	for _, obj := range objects {
		for _, rule := range rulesOf(obj) {
			for _, list := range [][]string{rule.groups, rule.resources, rule.verbs} {
				if slices.Contains(list, "*") {
					t.Errorf("%s %s holds a wildcard", obj.GetKind(), obj.GetName())
				}
			}
		}
	}
	local := findObject(t, objects, "Role", "bedrock-proxy")
	for _, rule := range rulesOf(local) {
		if slices.Contains(rule.resources, "secrets") && !reflect.DeepEqual(rule.names, []string{"bedrock-console-client"}) {
			t.Errorf("the Secret rule in bedrock-system names %v", rule.names)
		}
	}
	for _, obj := range objects {
		if obj.GetKind() == "Role" && obj.GetName() == "bedrock-proxy-store" && obj.GetNamespace() != "bedrock-proxy" {
			t.Errorf("the store Role lives in %s", obj.GetNamespace())
		}
	}
}

func TestProxyClusterRoleAggregates(t *testing.T) {
	objects := proxyManifest(t, "10-rbac.yaml")
	aggregate := findObject(t, objects, "ClusterRole", "bedrock-proxy")
	selectors, _, _ := unstructured.NestedSlice(aggregate.Object, "aggregationRule", "clusterRoleSelectors")
	selector, _ := selectors[0].(map[string]any)
	labels, _, _ := unstructured.NestedStringMap(selector, "matchLabels")
	if len(selectors) != 1 || !reflect.DeepEqual(labels, map[string]string{"bedrock.cloudyfolks.io/aggregate-to-proxy": "true"}) {
		t.Fatalf("selectors %v", selectors)
	}
	if _, hasRules, _ := unstructured.NestedSlice(aggregate.Object, "rules"); hasRules {
		t.Fatal("the aggregated ClusterRole must hold no rules")
	}
	base := findObject(t, objects, "ClusterRole", "bedrock-proxy-base")
	if base.GetLabels()["bedrock.cloudyfolks.io/aggregate-to-proxy"] != "true" {
		t.Fatalf("base labels %v", base.GetLabels())
	}
	binding := findObject(t, objects, "ClusterRoleBinding", "bedrock-proxy")
	roleName, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
	if roleName != "bedrock-proxy" {
		t.Fatalf("binding role %q", roleName)
	}
}

func TestProxyBindingsPointAtTheirRoles(t *testing.T) {
	objects := proxyManifest(t, "10-rbac.yaml")
	for _, name := range []string{"bedrock-proxy-store", "bedrock-proxy"} {
		obj := findObject(t, objects, "RoleBinding", name)
		role, _, _ := unstructured.NestedString(obj.Object, "roleRef", "name")
		if role != name {
			t.Errorf("RoleBinding %s points at %s", name, role)
		}
	}
}

func TestProxyServersTransportAndCertificate(t *testing.T) {
	objects := proxyManifest(t, "20-workload.yaml")
	transport := findObject(t, objects, "ServersTransport", "bedrock-proxy")
	serverName, _, _ := unstructured.NestedString(transport.Object, "spec", "serverName")
	roots, _, _ := unstructured.NestedSlice(transport.Object, "spec", "rootCAs")
	if serverName != "bedrock-proxy.bedrock-system.svc" || !reflect.DeepEqual(roots, []any{map[string]any{"secret": "bedrock-proxy-tls"}}) {
		t.Fatalf("transport %v %v", serverName, roots)
	}
	certificate := findObject(t, objects, "Certificate", "bedrock-proxy-tls")
	issuer, _, _ := unstructured.NestedString(certificate.Object, "spec", "issuerRef", "name")
	algorithm, _, _ := unstructured.NestedString(certificate.Object, "spec", "privateKey", "algorithm")
	size, _, _ := unstructured.NestedInt64(certificate.Object, "spec", "privateKey", "size")
	names, _, _ := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
	if issuer != "bedrock-ca" || algorithm != "ECDSA" || size != 256 ||
		!reflect.DeepEqual(names, []string{"bedrock-proxy.bedrock-system.svc", "bedrock-proxy.bedrock-system.svc.cluster.local"}) {
		t.Fatalf("certificate %s %s %d %v", issuer, algorithm, size, names)
	}
}

func TestProxyObjectsCarryTheGroupComponentLabel(t *testing.T) {
	for _, obj := range allProxyObjects(t) {
		if obj.GetNamespace() == "" && obj.GetKind() != "Namespace" && !strings.HasPrefix(obj.GetKind(), "Cluster") {
			t.Errorf("%s %s has no namespace", obj.GetKind(), obj.GetName())
		}
		if obj.GetLabels()["bedrock.cloudyfolks.io/component"] != "proxy" {
			t.Errorf("%s %s labels %v", obj.GetKind(), obj.GetName(), obj.GetLabels())
		}
	}
}
