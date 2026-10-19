package clusters

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const testNamespace = "t-acme-web"

func testCluster() v1alpha1.KubernetesCluster {
	return v1alpha1.KubernetesCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "prod", UID: types.UID("uid-1")},
		Spec: v1alpha1.KubernetesClusterSpec{
			Version: "v1.36.3+k0s.1",
			Network: v1alpha1.KubernetesClusterNetwork{SubnetRef: "default", PodCIDR: "100.64.0.0/16", ServiceCIDR: "100.65.0.0/16"},
			ControlPlane: v1alpha1.KubernetesControlPlane{
				SecurityGroups: []string{"k8s"},
			},
			Workers: []v1alpha1.KubernetesWorker{testWorker("a")},
		},
	}
}

func testWorker(name string) v1alpha1.KubernetesWorker {
	return v1alpha1.KubernetesWorker{
		Name:           name,
		Compute:        v1alpha1.KubernetesWorkerCompute{Cores: 2, MemoryMiB: 4096},
		RootDisk:       v1alpha1.KubernetesWorkerDisk{SizeGi: 40},
		Image:          v1alpha1.ImageRef{Name: "ubuntu-24.04", Scope: "cluster"},
		SecurityGroups: []string{"k8s", "extra"},
	}
}

var testLabels = map[string]string{"bedrock.cloudyfolks.io/tenant": "acme", "bedrock.cloudyfolks.io/project": "web"}

func renderTestControlPlane(t *testing.T, cluster v1alpha1.KubernetesCluster) *unstructured.Unstructured {
	t.Helper()
	obj, err := RenderControlPlane(ControlPlaneInput{Cluster: cluster, Labels: testLabels, Address: "10.100.0.10"})
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func nested[T any](t *testing.T, obj *unstructured.Unstructured, path ...string) T {
	t.Helper()
	value, found, err := unstructured.NestedFieldNoCopy(obj.Object, path...)
	if err != nil || !found {
		t.Fatalf("%s not found (%v)", strings.Join(path, "."), err)
	}
	typed, ok := value.(T)
	if !ok {
		t.Fatalf("%s has type %T", strings.Join(path, "."), value)
	}
	return typed
}

func TestRenderControlPlaneIsAV1Beta2Cluster(t *testing.T) {
	obj := renderTestControlPlane(t, testCluster())
	if obj.GetAPIVersion() != "k0smotron.io/v1beta2" || obj.GetKind() != "Cluster" || obj.GetName() != "prod" || obj.GetNamespace() != testNamespace {
		t.Fatalf("object %s %s %s/%s", obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace(), obj.GetName())
	}
	if obj.GetLabels()["bedrock.cloudyfolks.io/project"] != "web" {
		t.Errorf("labels %v", obj.GetLabels())
	}
	owners := obj.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "KubernetesCluster" || owners[0].Name != "prod" || owners[0].UID != "uid-1" || owners[0].Controller == nil || !*owners[0].Controller {
		t.Errorf("owners %+v", owners)
	}
}

func TestRenderControlPlaneSpecFields(t *testing.T) {
	obj := renderTestControlPlane(t, testCluster())
	spec := nested[map[string]any](t, obj, "spec")
	if spec["version"] != "v1.36.3+k0s.1" || spec["replicas"] != int64(1) || spec["externalAddress"] != "10.100.0.10" {
		t.Errorf("spec %v", spec)
	}
	service := nested[map[string]any](t, obj, "spec", "service")
	if service["type"] != "ClusterIP" || service["apiPort"] != int64(6443) || service["konnectivityPort"] != int64(8132) {
		t.Errorf("both ports must be the container ports: %v", service)
	}
	storage := nested[map[string]any](t, obj, "spec", "storage")
	kine := storage["kine"].(map[string]any)
	if storage["type"] != "kine" || kine["dataSourceURL"] != "sqlite:///var/lib/k0s/db/state.db" {
		t.Errorf("storage %v: etcd would add a second pod that a tenant-only pod cannot reach", storage)
	}
	persistence := nested[map[string]any](t, obj, "spec", "persistence")
	claim := persistence["persistentVolumeClaim"].(map[string]any)["spec"].(map[string]any)
	size := claim["resources"].(map[string]any)["requests"].(map[string]any)["storage"]
	if persistence["type"] != "pvc" || persistence["autoDeletePVCs"] != true || claim["storageClassName"] != "block" || size != "10Gi" {
		t.Errorf("persistence %v", persistence)
	}
	resources := nested[map[string]any](t, obj, "spec", "resources")
	requests, limits := resources["requests"].(map[string]any), resources["limits"].(map[string]any)
	if requests["cpu"] != "500m" || requests["memory"] != "1Gi" || limits["cpu"] != "2" || limits["memory"] != "4Gi" {
		t.Errorf("resources %v", resources)
	}
}

func TestRenderControlPlaneK0sConfigHasTheSANAndTheCIDRs(t *testing.T) {
	obj := renderTestControlPlane(t, testCluster())
	config := nested[map[string]any](t, obj, "spec", "k0sConfig")
	if config["apiVersion"] != "k0s.k0sproject.io/v1beta1" || config["kind"] != "ClusterConfig" {
		t.Errorf("config %v", config)
	}
	sans := nested[[]any](t, obj, "spec", "k0sConfig", "spec", "api", "sans")
	if len(sans) != 1 || sans[0] != "10.100.0.10" {
		t.Errorf("sans %v", sans)
	}
	net := nested[map[string]any](t, obj, "spec", "k0sConfig", "spec", "network")
	if net["provider"] != "kuberouter" || net["podCIDR"] != "100.64.0.0/16" || net["serviceCIDR"] != "100.65.0.0/16" {
		t.Errorf("network %v", net)
	}
}

func TestRenderControlPlanePatchHasThePodAttachment(t *testing.T) {
	obj := renderTestControlPlane(t, testCluster())
	patches := nested[[]any](t, obj, "spec", "patches")
	if len(patches) != 1 {
		t.Fatalf("patches %v", patches)
	}
	entry := patches[0].(map[string]any)
	target := entry["target"].(map[string]any)
	patch := entry["patch"].(map[string]any)
	if target["kind"] != "StatefulSet" || target["component"] != "control-plane" || patch["type"] != "strategic" {
		t.Fatalf("patch entry %v", entry)
	}
	var content map[string]any
	if err := sigyaml.Unmarshal([]byte(patch["content"].(string)), &content); err != nil {
		t.Fatal(err)
	}
	group := network.FabricName(testNamespace, "k8s")
	want := map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
		"annotations": map[string]any{
			"v1.multus-cni.io/default-network":                                    testNamespace + "/default",
			"default." + testNamespace + ".fabric.cloudyfolks.io/port_security":   "true",
			"default." + testNamespace + ".fabric.cloudyfolks.io/security_groups": group,
			"default." + testNamespace + ".fabric.cloudyfolks.io/ip_address":      "10.100.0.10",
		},
		"labels": map[string]any{network.SecurityGroupLabel("k8s"): "true"},
	}}}}
	if !equality.Semantic.DeepEqual(content, want) {
		t.Fatalf("patch content\n got %v\nwant %v", content, want)
	}
}

func TestRenderControlPlanePatchHasNoProbes(t *testing.T) {
	obj := renderTestControlPlane(t, testCluster())
	patches := nested[[]any](t, obj, "spec", "patches")
	content := patches[0].(map[string]any)["patch"].(map[string]any)["content"].(string)
	for _, word := range []string{"Probe", "containers", "httpGet", "tcpSocket"} {
		if strings.Contains(content, word) {
			t.Errorf("k0smotron renders exec probes (k0s status) already, the patch must not hold %q", word)
		}
	}
}

func TestRenderControlPlanePatchDoesNotChangeThePodForTheSameInput(t *testing.T) {
	first := renderTestControlPlane(t, testCluster())
	second := renderTestControlPlane(t, testCluster())
	if !equality.Semantic.DeepEqual(first.Object, second.Object) {
		t.Fatal("the render must be deterministic: a changed patch would restart the pod")
	}
}

func TestRenderControlPlaneUsesTheSubnetAndGroupsOfTheCluster(t *testing.T) {
	cluster := testCluster()
	cluster.Spec.Network.SubnetRef = "nodes"
	cluster.Spec.ControlPlane.SecurityGroups = []string{"b", "a"}
	obj := renderTestControlPlane(t, cluster)
	patches := nested[[]any](t, obj, "spec", "patches")
	content := patches[0].(map[string]any)["patch"].(map[string]any)["content"].(string)
	if !strings.Contains(content, testNamespace+"/nodes") {
		t.Errorf("subnet missing from %s", content)
	}
	sorted := []string{network.FabricName(testNamespace, "a"), network.FabricName(testNamespace, "b")}
	slices.Sort(sorted)
	if !strings.Contains(content, strings.Join(sorted, ",")) {
		t.Errorf("groups %v missing from %s", sorted, content)
	}
}

func TestRenderControlPlaneDoesNotChangeItsInput(t *testing.T) {
	cluster := testCluster()
	before := cluster.DeepCopy()
	renderTestControlPlane(t, cluster)
	if !equality.Semantic.DeepEqual(before, &cluster) {
		t.Fatal("input changed")
	}
}

func TestRenderTokenIsAOneDayWorkerRequest(t *testing.T) {
	obj := RenderToken(testCluster(), "a", testLabels)
	if obj.GetAPIVersion() != "k0smotron.io/v1beta2" || obj.GetKind() != "JoinTokenRequest" || obj.GetName() != "prod-a" || obj.GetNamespace() != testNamespace {
		t.Fatalf("object %v", obj.Object)
	}
	spec := nested[map[string]any](t, obj, "spec")
	if spec["clusterName"] != "prod" || spec["expiry"] != "24h" || spec["role"] != "worker" {
		t.Errorf("spec %v", spec)
	}
	if obj.GetLabels()["bedrock.cloudyfolks.io/cluster-worker"] != "a" || obj.GetLabels()["bedrock.cloudyfolks.io/cluster"] != "prod" {
		t.Errorf("labels %v", obj.GetLabels())
	}
}

func TestRenderCloudInitForAVersion(t *testing.T) {
	got := RenderCloudInit("TOKEN123", "v1.36.3+k0s.1")
	want := `#cloud-config
write_files:
  - path: /etc/k0s/join.token
    permissions: "0600"
    content: |
      TOKEN123
runcmd:
  - curl -sSLf https://get.k0s.sh | K0S_VERSION=v1.36.3+k0s.1 sh
  - k0s install worker --token-file /etc/k0s/join.token
  - k0s start
`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	var parsed map[string]any
	if err := sigyaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("the cloud-config must be YAML: %v", err)
	}
	files := parsed["write_files"].([]any)[0].(map[string]any)
	if files["content"] != "TOKEN123\n" {
		t.Errorf("the token file content is %q", files["content"])
	}
}

func TestRenderCloudInitSecret(t *testing.T) {
	secret := RenderCloudInitSecret(testCluster(), "a", "TOKEN123", testLabels)
	if secret.Name != "prod-a-cloudinit" || secret.Namespace != testNamespace {
		t.Fatalf("secret %s/%s", secret.Namespace, secret.Name)
	}
	if secret.StringData["userdata"] != RenderCloudInit("TOKEN123", "v1.36.3+k0s.1") {
		t.Errorf("userdata %q", secret.StringData["userdata"])
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != "prod" || secret.OwnerReferences[0].Kind != "KubernetesCluster" {
		t.Errorf("owners %+v", secret.OwnerReferences)
	}
	if secret.Labels["bedrock.cloudyfolks.io/cluster-worker"] != "a" || secret.Labels["bedrock.cloudyfolks.io/project"] != "web" {
		t.Errorf("labels %v", secret.Labels)
	}
}

func TestRenderWorkerFields(t *testing.T) {
	cluster := testCluster()
	worker := cluster.Spec.Workers[0]
	worker.Address = "10.100.0.50"
	worker.SSHKeys = []string{"key-a"}
	obj, err := RenderWorker(WorkerInput{Cluster: cluster, Worker: worker, Labels: testLabels})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetAPIVersion() != "bedrock.cloudyfolks.io/v1alpha1" || obj.GetKind() != "VirtualMachine" || obj.GetName() != "prod-a" || obj.GetNamespace() != testNamespace {
		t.Fatalf("object %v", obj.Object)
	}
	spec := nested[map[string]any](t, obj, "spec")
	compute := spec["compute"].(map[string]any)
	if compute["cores"] != int64(2) || compute["memoryMiB"] != int64(4096) {
		t.Errorf("compute %v", compute)
	}
	if spec["rootDisk"].(map[string]any)["sizeGi"] != int64(40) {
		t.Errorf("rootDisk %v", spec["rootDisk"])
	}
	image := spec["image"].(map[string]any)
	if image["name"] != "ubuntu-24.04" || image["scope"] != "cluster" {
		t.Errorf("image %v", image)
	}
	networks := spec["networks"].([]any)
	nic := networks[0].(map[string]any)
	if len(networks) != 1 || nic["subnetRef"] != "default" || nic["address"] != "10.100.0.50" || !slices.Equal(nic["securityGroups"].([]any), []any{"k8s", "extra"}) {
		t.Errorf("networks %v", networks)
	}
	if spec["cloudInit"].(map[string]any)["secretRef"] != "prod-a-cloudinit" {
		t.Errorf("cloudInit %v", spec["cloudInit"])
	}
	if spec["running"] != true || !slices.Equal(spec["sshKeys"].([]any), []any{"key-a"}) {
		t.Errorf("spec %v", spec)
	}
	owners := obj.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != "prod" || owners[0].Kind != "KubernetesCluster" {
		t.Errorf("owners %+v", owners)
	}
	if obj.GetLabels()["bedrock.cloudyfolks.io/cluster"] != "prod" || obj.GetLabels()["bedrock.cloudyfolks.io/cluster-worker"] != "a" || obj.GetLabels()["bedrock.cloudyfolks.io/tenant"] != "acme" {
		t.Errorf("labels %v", obj.GetLabels())
	}
}

func TestRenderWorkerWithoutAddressOrKeys(t *testing.T) {
	cluster := testCluster()
	obj, err := RenderWorker(WorkerInput{Cluster: cluster, Worker: cluster.Spec.Workers[0]})
	if err != nil {
		t.Fatal(err)
	}
	nic := nested[[]any](t, obj, "spec", "networks")[0].(map[string]any)
	if _, found := nic["address"]; found {
		t.Errorf("no address: %v", nic)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "sshKeys"); found {
		t.Error("no ssh keys")
	}
}

func TestRenderWorkerStopsWhenTheClusterIsSuspended(t *testing.T) {
	cluster := testCluster()
	cluster.Spec.Suspended = true
	obj, err := RenderWorker(WorkerInput{Cluster: cluster, Worker: cluster.Spec.Workers[0]})
	if err != nil {
		t.Fatal(err)
	}
	if nested[bool](t, obj, "spec", "running") {
		t.Fatal("a suspended cluster has stopped workers")
	}
}

func TestRenderWorkerKeepsTheImageScopeDefault(t *testing.T) {
	cluster := testCluster()
	worker := cluster.Spec.Workers[0]
	worker.Image = v1alpha1.ImageRef{Name: "custom"}
	obj, err := RenderWorker(WorkerInput{Cluster: cluster, Worker: worker})
	if err != nil {
		t.Fatal(err)
	}
	if nested[string](t, obj, "spec", "image", "scope") != "cluster" {
		t.Fatal("scope")
	}
}

func TestNamesStayWithinTheLimitsForTheLongestAllowedPair(t *testing.T) {
	cluster := strings.Repeat("c", 40)
	worker := strings.Repeat("w", 11)
	for name, value := range map[string]string{
		"vm":            WorkerName(cluster, worker),
		"token request": WorkerName(cluster, worker),
		"statefulset":   StatefulSetName(cluster),
		"pod":           ControlPlanePodName(cluster),
		"kubeconfig":    KubeconfigSecretName(cluster),
		"cloud-init":    CloudInitSecretName(cluster, worker),
	} {
		if problems := validation.IsDNS1123Label(value); len(problems) > 0 && name != "cloud-init" {
			t.Errorf("%s name %q: %v", name, value, problems)
		}
	}
	if len(WorkerName(cluster, worker)) != 52 {
		t.Errorf("the VM name of the longest pair is %d characters", len(WorkerName(cluster, worker)))
	}
	if problems := validation.IsDNS1123Subdomain(CloudInitSecretName(cluster, worker)); len(problems) > 0 {
		t.Errorf("cloud-init secret name: %v", problems)
	}
	if len(ControlPlanePodName(cluster)) > 63 {
		t.Errorf("the pod name %q is above the label limit", ControlPlanePodName(cluster))
	}
	if ControlPlaneClaimName("prod") != "kmc-prod-kmc-prod-0" {
		t.Errorf("claim %q", ControlPlaneClaimName("prod"))
	}
}
