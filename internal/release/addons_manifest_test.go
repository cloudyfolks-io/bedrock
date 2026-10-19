package release

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func buildReal(t *testing.T, names ...string) (Bundle, string) {
	t.Helper()
	cfg, err := LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	selected := cfg.Components[:0:0]
	for _, component := range cfg.Components {
		if slices.Contains(names, component.Name) || component.Name == "crds" {
			selected = append(selected, component)
		}
	}
	cfg.Components = selected
	out := t.TempDir()
	opts := BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-io/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("..", ".."), CacheDir: t.TempDir()}
	if err := Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	return bundle, out
}

func objectNamed(objects []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	for _, obj := range objects {
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}
	return nil
}

func TestKataComponentRendersTheDaemonSetAndRuntimeClasses(t *testing.T) {
	bundle, _ := buildReal(t, "kata")
	objects := bundle.Addons["kata"]
	want := map[string]string{
		"ServiceAccount":     "kata-deploy-sa",
		"ConfigMap":          "kata-kata-deploy-state",
		"ClusterRole":        "kata-deploy-role",
		"ClusterRoleBinding": "kata-deploy-rb",
		"DaemonSet":          "kata-deploy",
	}
	for kind, name := range want {
		if objectNamed(objects, kind, name) == nil {
			t.Errorf("missing %s %s", kind, name)
		}
	}
	for _, name := range []string{"kata-qemu", "kata"} {
		class := objectNamed(objects, "RuntimeClass", name)
		if class == nil {
			t.Fatalf("missing RuntimeClass %s", name)
		}
		if handler, _, _ := unstructured.NestedString(class.Object, "handler"); handler != "kata-qemu" {
			t.Errorf("RuntimeClass %s handler %q", name, handler)
		}
	}
	if len(objects) != 8 {
		t.Fatalf("kinds %v", kinds(objects))
	}
	daemonSet := objectNamed(objects, "DaemonSet", "kata-deploy")
	if daemonSet.GetNamespace() != "kube-system" {
		t.Errorf("namespace %q", daemonSet.GetNamespace())
	}
	selector, _, _ := unstructured.NestedStringMap(daemonSet.Object, "spec", "template", "spec", "nodeSelector")
	if selector["bedrock.cloudyfolks.io/kvm"] != "true" {
		t.Errorf("nodeSelector %v", selector)
	}
	containers, _, _ := unstructured.NestedSlice(daemonSet.Object, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]any)
	if container["imagePullPolicy"] != "IfNotPresent" {
		t.Errorf("imagePullPolicy %v", container["imagePullPolicy"])
	}
	distribution := ""
	for _, env := range container["env"].([]any) {
		if entry := env.(map[string]any); entry["name"] == "K8S_DISTRIBUTION" {
			distribution, _ = entry["value"].(string)
		}
	}
	if distribution != "k0s" {
		t.Errorf("K8S_DISTRIBUTION %q", distribution)
	}
}

func TestKataComponentTunesTheRuntime(t *testing.T) {
	bundle, _ := buildReal(t, "kata")
	objects := bundle.Addons["kata"]
	for _, name := range []string{"kata-qemu", "kata"} {
		class := objectNamed(objects, "RuntimeClass", name)
		if class == nil {
			t.Fatalf("missing RuntimeClass %s", name)
		}
		overhead, _, _ := unstructured.NestedStringMap(class.Object, "overhead", "podFixed")
		if overhead["memory"] != "512Mi" || overhead["cpu"] != "250m" {
			t.Errorf("RuntimeClass %s overhead %v", name, overhead)
		}
	}
	config := objectNamed(objects, "ConfigMap", "kata-deploy-custom-configs")
	if config == nil {
		t.Fatalf("missing the drop-in ConfigMap: %v", kinds(objects))
	}
	dropIn, _, _ := unstructured.NestedString(config.Object, "data", "dropin-qemu.toml")
	for _, line := range []string{"[hypervisor.qemu]", "default_memory = 512", `virtio_fs_extra_args = ["--thread-pool-size=1", "--announce-submounts", "--xattr"]`} {
		if !strings.Contains(dropIn, line) {
			t.Errorf("drop-in does not hold %q: %s", line, dropIn)
		}
	}
}

func TestKataComponentHasNoHooksAndNoNamespace(t *testing.T) {
	bundle, _ := buildReal(t, "kata")
	for _, obj := range bundle.Addons["kata"] {
		if obj.GetKind() == "Namespace" {
			t.Errorf("namespace %s", obj.GetName())
		}
		for key := range obj.GetAnnotations() {
			if strings.HasPrefix(key, "helm.sh/hook") {
				t.Errorf("%s %s has %s", obj.GetKind(), obj.GetName(), key)
			}
		}
	}
}

func TestKataImageIsInTheImageList(t *testing.T) {
	bundle, out := buildReal(t, "kata")
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), "quay.io/kata-containers/kata-deploy:4.2.0") || !slices.Contains(bundle.Images, "quay.io/kata-containers/kata-deploy:4.2.0") {
		t.Fatalf("images %v", bundle.Images)
	}
}

func TestClustersAddonShipsTheK0smotronOperator(t *testing.T) {
	bundle, out := buildReal(t, "clusters")
	objects := bundle.Addons["clusters"]
	for kind, name := range map[string]string{
		"Namespace": "k0smotron", "Deployment": "k0smotron-controller-manager", "ServiceAccount": "k0smotron-controller-manager",
		"Certificate": "k0smotron-serving-cert", "MutatingWebhookConfiguration": "k0smotron-mutating-webhook-configuration",
		"ValidatingWebhookConfiguration": "k0smotron-validating-webhook-configuration",
	} {
		if objectNamed(objects, kind, name) == nil {
			t.Errorf("missing %s %s: %v", kind, name, kinds(objects))
		}
	}
	for _, obj := range objects {
		if obj.GetKind() == "CustomResourceDefinition" {
			t.Errorf("CRD %s stays in the addon group", obj.GetName())
		}
	}
	crds := 0
	for _, group := range bundle.Groups {
		for _, obj := range group.Objects {
			if group.Name == "crds" && obj.GetKind() == "CustomResourceDefinition" && strings.HasSuffix(obj.GetName(), ".k0smotron.io") {
				crds++
			}
		}
	}
	if crds != 2 {
		t.Errorf("k0smotron.io CRDs in the crds group: %d", crds)
	}
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"quay.io/k0sproject/k0smotron:v2.1.0", "quay.io/k0sproject/k0s:v1.36.3-k0s.1"} {
		if !strings.Contains(string(raw), image) || !slices.Contains(bundle.Images, image) {
			t.Errorf("image %s is not listed", image)
		}
	}
}

func TestCNPGComponentRendersTheOperatorAndThePlugin(t *testing.T) {
	bundle, _ := buildReal(t, "cnpg")
	objects := bundle.Addons["cnpg"]
	want := map[string]string{
		"Namespace":  "cnpg",
		"Deployment": "barman-cloud",
		"Issuer":     "selfsigned-issuer",
	}
	for kind, name := range want {
		if objectNamed(objects, kind, name) == nil {
			t.Errorf("missing %s %s", kind, name)
		}
	}
	operator := 0
	for _, obj := range objects {
		if obj.GetKind() == "Deployment" && obj.GetNamespace() == "cnpg" {
			operator++
		}
		if obj.GetNamespace() == "cnpg-system" {
			t.Errorf("%s %s is in cnpg-system", obj.GetKind(), obj.GetName())
		}
	}
	if operator != 2 {
		t.Errorf("deployments in cnpg: %d", operator)
	}
}

func TestCNPGComponentPutsCRDsInTheBaseGroup(t *testing.T) {
	bundle, _ := buildReal(t, "cnpg")
	for _, obj := range bundle.Addons["cnpg"] {
		if obj.GetKind() == "CustomResourceDefinition" {
			t.Errorf("addon holds CRD %s", obj.GetName())
		}
	}
	var crds []string
	for _, group := range bundle.Groups {
		if group.Name != "crds" {
			continue
		}
		for _, obj := range group.Objects {
			crds = append(crds, obj.GetName())
		}
	}
	for _, name := range []string{"clusters.postgresql.cnpg.io", "scheduledbackups.postgresql.cnpg.io", "backups.postgresql.cnpg.io", "objectstores.barmancloud.cnpg.io"} {
		if !slices.Contains(crds, name) {
			t.Errorf("missing CRD %s", name)
		}
	}
}

func TestCNPGComponentHasNoHooks(t *testing.T) {
	bundle, _ := buildReal(t, "cnpg")
	for _, obj := range bundle.Addons["cnpg"] {
		for key := range obj.GetAnnotations() {
			if strings.HasPrefix(key, "helm.sh/hook") {
				t.Errorf("%s %s has %s", obj.GetKind(), obj.GetName(), key)
			}
		}
	}
}

func TestCNPGImagesAreInTheImageList(t *testing.T) {
	bundle, out := buildReal(t, "cnpg")
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{
		"ghcr.io/cloudnative-pg/cloudnative-pg:1.30.1",
		"ghcr.io/cloudnative-pg/plugin-barman-cloud:v0.13.0",
		"ghcr.io/cloudnative-pg/plugin-barman-cloud-sidecar:v0.13.0",
	} {
		if !contains(string(raw), image) || !slices.Contains(bundle.Images, image) {
			t.Errorf("image %s missing from %v", image, bundle.Images)
		}
	}
	found := false
	for _, image := range bundle.Images {
		found = found || strings.HasPrefix(image, "docker.io/library/haproxy:") && strings.Contains(image, "@sha256:")
	}
	if !found {
		t.Errorf("no digest-pinned HAProxy image in %v", bundle.Images)
	}
	for _, major := range []string{"16", "17", "18"} {
		found := false
		for _, image := range bundle.Images {
			found = found || strings.HasPrefix(image, "ghcr.io/cloudnative-pg/postgresql:"+major+".") && strings.Contains(image, "@sha256:")
		}
		if !found {
			t.Errorf("no digest-pinned PostgreSQL %s image in %v", major, bundle.Images)
		}
	}
}

func TestCNPGComponentHoldsTheDatabaseCA(t *testing.T) {
	bundle, _ := buildReal(t, "cnpg")
	objects := bundle.Addons["cnpg"]
	if objectNamed(objects, "ClusterIssuer", "bedrock-database-selfsigned") == nil {
		t.Error("missing the self-signed issuer")
	}
	ca := objectNamed(objects, "Certificate", "bedrock-database-ca")
	if ca == nil || ca.GetNamespace() != "cert-manager" {
		t.Fatalf("CA certificate %v", ca)
	}
	if isCA, _, _ := unstructured.NestedBool(ca.Object, "spec", "isCA"); !isCA {
		t.Error("the CA certificate is not a CA")
	}
	issuer := objectNamed(objects, "ClusterIssuer", "bedrock-database-ca")
	if issuer == nil {
		t.Fatal("missing the CA issuer")
	}
	if name, _, _ := unstructured.NestedString(issuer.Object, "spec", "ca", "secretName"); name != "bedrock-database-ca" {
		t.Errorf("the CA issuer reads the secret %q", name)
	}
}

func TestRegistryComponentRendersHarborWithoutHooksOrCRDs(t *testing.T) {
	bundle, _ := buildReal(t, "registry")
	objects := bundle.Addons["registry"]
	for _, name := range []string{"harbor-core", "harbor-registry", "harbor-jobservice", "harbor-portal", "harbor-exporter"} {
		if objectNamed(objects, "Deployment", name) == nil {
			t.Errorf("missing Deployment %s", name)
		}
	}
	if objectNamed(objects, "StatefulSet", "harbor-redis") == nil {
		t.Error("missing StatefulSet harbor-redis")
	}
	for _, obj := range objects {
		if obj.GetKind() == "CustomResourceDefinition" {
			t.Errorf("addon holds CRD %s", obj.GetName())
		}
		if obj.GetKind() != "Namespace" && obj.GetNamespace() != "bedrock-registry" {
			t.Errorf("%s %s is in %q", obj.GetKind(), obj.GetName(), obj.GetNamespace())
		}
		for key := range obj.GetAnnotations() {
			if strings.HasPrefix(key, "helm.sh/hook") {
				t.Errorf("%s %s has %s", obj.GetKind(), obj.GetName(), key)
			}
		}
	}
	for _, name := range []string{"harbor-database", "harbor-trivy"} {
		if objectNamed(objects, "StatefulSet", name) != nil {
			t.Errorf("the addon runs %s", name)
		}
	}
}

func TestRegistryImagesAreInTheImageList(t *testing.T) {
	bundle, out := buildReal(t, "registry")
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"harbor-core", "harbor-portal", "harbor-jobservice", "harbor-registryctl", "harbor-exporter", "registry-photon", "redis-photon", "nginx-photon"} {
		image := "goharbor/" + name + ":v2.14.4"
		if !contains(string(raw), image) || !slices.Contains(bundle.Images, image) {
			t.Errorf("image %s missing from %v", image, bundle.Images)
		}
	}
	for _, image := range bundle.Images {
		if strings.Contains(image, "harbor-db") || strings.Contains(image, "trivy") {
			t.Errorf("unused image %s is in the list", image)
		}
	}
}

func TestPerconaComponentRendersTheOperator(t *testing.T) {
	bundle, _ := buildReal(t, "percona-ps")
	objects := bundle.Addons["percona-ps"]
	for kind, name := range map[string]string{"Namespace": "ps-operator", "Deployment": "percona-ps-ps-operator"} {
		if objectNamed(objects, kind, name) == nil {
			t.Errorf("missing %s %s", kind, name)
		}
	}
	for _, obj := range objects {
		if obj.GetKind() == "CustomResourceDefinition" {
			t.Errorf("addon holds CRD %s", obj.GetName())
		}
		for key := range obj.GetAnnotations() {
			if strings.HasPrefix(key, "helm.sh/hook") {
				t.Errorf("%s %s has %s", obj.GetKind(), obj.GetName(), key)
			}
		}
	}
	deployment := objectNamed(objects, "Deployment", "percona-ps-ps-operator")
	if deployment == nil {
		return
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	env, _, _ := unstructured.NestedSlice(containers[0].(map[string]any), "env")
	for _, item := range env {
		entry := item.(map[string]any)
		if entry["name"] == "WATCH_NAMESPACE" && entry["value"] != "bedrock-dbaas" {
			t.Errorf("WATCH_NAMESPACE is %v", entry["value"])
		}
	}
}

func TestPerconaComponentPutsCRDsInTheBaseGroup(t *testing.T) {
	bundle, _ := buildReal(t, "percona-ps")
	var crds []string
	for _, group := range bundle.Groups {
		if group.Name != "crds" {
			continue
		}
		for _, obj := range group.Objects {
			crds = append(crds, obj.GetName())
		}
	}
	for _, name := range []string{"perconaservermysqls.ps.percona.com", "perconaservermysqlbackups.ps.percona.com", "perconaservermysqlrestores.ps.percona.com"} {
		if !slices.Contains(crds, name) {
			t.Errorf("missing CRD %s", name)
		}
	}
}

func TestPerconaImagesAreInTheImageList(t *testing.T) {
	bundle, out := buildReal(t, "percona-ps")
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{
		"docker.io/percona/percona-server-mysql-operator:1.2.0@sha256:28bfc38c1d4b642e860fda46aa7a964fc2de11a93a6dd05fcd7de4fd782982c8",
		"docker.io/percona/percona-server:8.4.10-10.1@sha256:01cf1439241e11c5d49312db1b18ee01a5947869c1a1dec18417d88ffaeb5717",
		"docker.io/percona/percona-server:9.7.2-2.1@sha256:ad263f3a9b964131492485d06b8663de343d58c68063abd40a3da07fbf8f3359",
		"docker.io/percona/percona-xtrabackup:8.4.0-6.1@sha256:f4fc511cca67d1ba0aef6752c53bdd25383235cb58f1d28e570adbc4bb67d374",
		"docker.io/percona/percona-xtrabackup:9.7.1@sha256:41bd1636340a436012411b82b98c9d70a54fe600c29424d75749c53ca2e721c7",
		"docker.io/percona/haproxy:2.8.18-1@sha256:726b65930d86f7342eb6bce97a38b9af934c935345e4524afda6b65c2b3213b3",
		"docker.io/percona/percona-toolkit:3.7.1-4@sha256:9ed7a2025946ed326fb26fbfa45b49c81658be5bfe87a0ba5c34441df05e8dde",
	} {
		if !contains(string(raw), image) || !slices.Contains(bundle.Images, image) {
			t.Errorf("image %s missing from %v", image, bundle.Images)
		}
	}
}

func TestValkeyImagesAreInTheImageList(t *testing.T) {
	bundle, out := buildReal(t, "bedrock")
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{
		"docker.io/valkey/valkey:8.1.10@sha256:640c5e62cea04b6d6f2084232651d0cc70362d31f4f805e7be94dbed6855e8f2",
		"docker.io/valkey/valkey:9.0.6@sha256:63346cb24a61221e76bdf41acce99b3968a9fa83d8122144deab45394b27b4f2",
		"docker.io/valkey/valkey:9.1.2@sha256:418652cfb58ef879d4978c33553735d7147016032d5aefaa14c828e611eb9dfd",
		"docker.io/oliver006/redis_exporter:v1.94.0@sha256:0842ef7110e62b40bbde2d5c11f38c6fbd99f7100c75e3866637c1b335a1eff7",
	} {
		if !contains(string(raw), image) || !slices.Contains(bundle.Images, image) {
			t.Errorf("image %s missing from %v", image, bundle.Images)
		}
	}
}
