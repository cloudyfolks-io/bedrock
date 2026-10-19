package registry

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func baseConfig() Config {
	return Config{Host: "registry.example.com", DBInstances: 2, StorageClass: "block", Bucket: "bedrock-registry", BackupBucket: "bedrock-database-backups"}
}

func find(t *testing.T, objects []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, obj := range objects {
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}
	t.Fatalf("missing %s %s in %v", kind, name, names(objects))
	return nil
}

func matches(t *testing.T, route *unstructured.Unstructured) []string {
	t.Helper()
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
	out := []string{}
	for _, item := range rules {
		match, _, _ := unstructured.NestedString(item.(map[string]any), "match")
		out = append(out, match)
	}
	return out
}

func TestRoutesForOneHost(t *testing.T) {
	objects := RouteObjects(baseConfig())
	if got := names(objects); !slices.Equal(got, []string{"Certificate/registry-public", "IngressRoute/harbor-public"}) {
		t.Fatalf("objects %v", got)
	}
	want := []string{
		"Host(`registry.example.com`) && (PathPrefix(`/v2/`) || PathPrefix(`/service/`) || PathPrefix(`/c/`))",
		"Host(`registry.example.com`) && PathPrefix(`/api/`)",
	}
	if got := matches(t, find(t, objects, "IngressRoute", "harbor-public")); !slices.Equal(got, want) {
		t.Errorf("matches %v", got)
	}
	route := find(t, objects, "IngressRoute", "harbor-public")
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
	for _, item := range rules {
		rule := item.(map[string]any)
		if _, has := rule["middlewares"]; has {
			t.Errorf("the primary host has middlewares: %v", rule)
		}
		services := rule["services"].([]any)[0].(map[string]any)
		if services["name"] != "harbor-core" || services["port"] != int64(80) {
			t.Errorf("service %v", services)
		}
	}
	entry, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
	if !slices.Equal(entry, []string{"websecure"}) {
		t.Errorf("entry points %v", entry)
	}
	secret, _, _ := unstructured.NestedString(route.Object, "spec", "tls", "secretName")
	if secret != "registry-public-tls" {
		t.Errorf("tls secret %q", secret)
	}
}

func TestRoutesForTwoAliasesRewriteTheRealm(t *testing.T) {
	config := baseConfig()
	config.Aliases = []string{"mirror.example.com", "r.staging.example.com"}
	objects := RouteObjects(config)
	want := []string{
		"Certificate/registry-public",
		"IngressRoute/harbor-public",
		"IngressRoute/harbor-public-mirror-example-com",
		"IngressRoute/harbor-public-r-staging-example-com",
		"Middleware/harbor-realm-mirror-example-com",
		"Middleware/harbor-realm-r-staging-example-com",
	}
	if got := names(objects); !slices.Equal(got, want) {
		t.Fatalf("objects %v", got)
	}
	header, _, _ := unstructured.NestedString(find(t, objects, "Middleware", "harbor-realm-mirror-example-com").Object, "spec", "headers", "customResponseHeaders", "WWW-Authenticate")
	if header != `Bearer realm="https://mirror.example.com/service/token",service="harbor-registry"` {
		t.Errorf("realm header %q", header)
	}
	alias := find(t, objects, "IngressRoute", "harbor-public-mirror-example-com")
	rules, _, _ := unstructured.NestedSlice(alias.Object, "spec", "routes")
	first := rules[0].(map[string]any)["middlewares"].([]any)[0].(map[string]any)
	if first["name"] != "harbor-realm-mirror-example-com" {
		t.Errorf("alias middleware %v", first)
	}
	if _, has := rules[1].(map[string]any)["middlewares"]; has {
		t.Error("the api rule of an alias has a middleware")
	}
	dns, _, _ := unstructured.NestedStringSlice(find(t, objects, "Certificate", "registry-public").Object, "spec", "dnsNames")
	if !slices.Equal(dns, []string{"registry.example.com", "mirror.example.com", "r.staging.example.com"}) {
		t.Errorf("dns names %v", dns)
	}
}

func TestRoutesForAnAdminHostNeedTheAllowList(t *testing.T) {
	config := baseConfig()
	config.AdminHost = "harbor.example.com"
	if got := names(RouteObjects(config)); slices.Contains(got, "IngressRoute/harbor-admin") {
		t.Fatalf("an admin route exists without an allow list: %v", got)
	}
	config.AdminAllow = []string{"10.0.0.0/8", "192.168.1.0/24"}
	objects := RouteObjects(config)
	want := []string{
		"Certificate/registry-public",
		"IngressRoute/harbor-admin",
		"IngressRoute/harbor-public",
		"Middleware/harbor-admin-allow",
		"Middleware/harbor-realm-harbor-example-com",
	}
	if got := names(objects); !slices.Equal(got, want) {
		t.Fatalf("objects %v", got)
	}
	ranges, _, _ := unstructured.NestedStringSlice(find(t, objects, "Middleware", "harbor-admin-allow").Object, "spec", "ipAllowList", "sourceRange")
	if !slices.Equal(ranges, []string{"10.0.0.0/8", "192.168.1.0/24"}) {
		t.Errorf("source range %v", ranges)
	}
	admin := find(t, objects, "IngressRoute", "harbor-admin")
	rules, _, _ := unstructured.NestedSlice(admin.Object, "spec", "routes")
	if len(rules) != 3 {
		t.Fatalf("rules %v", rules)
	}
	for i, item := range rules {
		rule := item.(map[string]any)
		first := rule["middlewares"].([]any)[0].(map[string]any)["name"]
		if first != "harbor-admin-allow" {
			t.Errorf("rule %d does not start with the allow list: %v", i, rule["middlewares"])
		}
	}
	portal := rules[2].(map[string]any)["services"].([]any)[0].(map[string]any)["name"]
	if portal != "harbor-portal" {
		t.Errorf("the last rule goes to %v", portal)
	}
	dns, _, _ := unstructured.NestedStringSlice(find(t, objects, "Certificate", "registry-public").Object, "spec", "dnsNames")
	if !slices.Contains(dns, "harbor.example.com") {
		t.Errorf("dns names %v", dns)
	}
}

func TestPublicCertificateIssuerFollowsTheTLSMode(t *testing.T) {
	config := baseConfig()
	issuer := func() string {
		name, _, _ := unstructured.NestedString(PublicCertificate(config).Object, "spec", "issuerRef", "name")
		return name
	}
	if issuer() != "bedrock-ca" {
		t.Errorf("issuer %q", issuer())
	}
	config.TLSMode = "LetsEncrypt"
	if issuer() != "letsencrypt" {
		t.Errorf("issuer %q", issuer())
	}
}

func TestTokenCertificateIsSelfSignedAndNeverRotatesItsKey(t *testing.T) {
	cert := TokenCertificate()
	issuer, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
	rotation, _, _ := unstructured.NestedString(cert.Object, "spec", "privateKey", "rotationPolicy")
	secret, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	if cert.GetNamespace() != "bedrock-registry" || issuer != "bedrock-selfsigned" || rotation != "Never" || secret != "harbor-token" {
		t.Errorf("certificate %v", cert.Object)
	}
}

func TestDatabaseObjects(t *testing.T) {
	objects := DatabaseObjects(baseConfig(), "http://rgw.example.svc:80")
	if got := names(objects); !slices.Equal(got, []string{"Cluster/harbor-db", "ObjectStore/harbor-db-backups", "ScheduledBackup/harbor-db-daily"}) {
		t.Fatalf("objects %v", got)
	}
	cluster := find(t, objects, "Cluster", "harbor-db")
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	image, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName")
	class, _, _ := unstructured.NestedString(cluster.Object, "spec", "storage", "storageClass")
	owner, _, _ := unstructured.NestedString(cluster.Object, "spec", "bootstrap", "initdb", "owner")
	secret, _, _ := unstructured.NestedString(cluster.Object, "spec", "bootstrap", "initdb", "secret", "name")
	if instances != 2 || image != DatabaseImage || class != "block" || owner != "harbor" || secret != "harbor-db-credentials" {
		t.Errorf("cluster %v", cluster.Object["spec"])
	}
	plugins, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins")
	plugin := plugins[0].(map[string]any)
	if plugin["name"] != "barman-cloud.cloudnative-pg.io" || plugin["isWALArchiver"] != true || plugin["parameters"].(map[string]any)["barmanObjectName"] != "harbor-db-backups" {
		t.Errorf("plugin %v", plugin)
	}
	store := find(t, objects, "ObjectStore", "harbor-db-backups")
	destination, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "destinationPath")
	endpoint, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "endpointURL")
	keyName, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "s3Credentials", "accessKeyId", "name")
	if destination != "s3://bedrock-database-backups/registry/" || endpoint != "http://rgw.example.svc:80" || keyName != "database-backups-s3" {
		t.Errorf("object store %v", store.Object["spec"])
	}
	backup := find(t, objects, "ScheduledBackup", "harbor-db-daily")
	method, _, _ := unstructured.NestedString(backup.Object, "spec", "method")
	target, _, _ := unstructured.NestedString(backup.Object, "spec", "cluster", "name")
	if method != "plugin" || target != "harbor-db" {
		t.Errorf("scheduled backup %v", backup.Object["spec"])
	}
	for _, obj := range objects {
		if obj.GetNamespace() != "bedrock-registry" {
			t.Errorf("%s is in %q", obj.GetName(), obj.GetNamespace())
		}
	}
}

func TestEveryObjectIsLabelledAsManaged(t *testing.T) {
	config := baseConfig()
	config.Aliases = []string{"a.example.com"}
	all := append(RouteObjects(config), DatabaseObjects(config, "http://x")...)
	all = append(all, TokenCertificate())
	for _, obj := range all {
		if obj.GetLabels()["app.kubernetes.io/managed-by"] != "bedrock-registry" {
			t.Errorf("%s %s is not labelled", obj.GetKind(), obj.GetName())
		}
	}
}

func TestParseConfigUsesDefaultsAndValidates(t *testing.T) {
	config, err := ParseConfig(map[string]string{"registry.host": "r.example.com", "registry.aliases": "a.example.com,b.example.com", "platform.tls-mode": "LetsEncrypt"})
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "r.example.com" || !slices.Equal(config.Aliases, []string{"a.example.com", "b.example.com"}) || config.DBInstances != 2 || config.StorageClass != "block" ||
		config.Bucket != "bedrock-registry" || config.BackupBucket != "bedrock-database-backups" || config.TLSMode != "LetsEncrypt" {
		t.Errorf("config %+v", config)
	}
	if _, err := ParseConfig(map[string]string{"registry.db-instances": "9"}); err == nil || !strings.Contains(err.Error(), "9") {
		t.Errorf("error %v", err)
	}
}
