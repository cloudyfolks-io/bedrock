package operator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const k0sCoreDNSObjects = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: coredns
  namespace: kube-system
  labels: {k0s.k0sproject.io/stack: coredns}
  annotations: {k0s.k0sproject.io/stack-checksum: sum-sa}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: system-coredns
  labels: {k0s.k0sproject.io/stack: coredns}
  annotations: {k0s.k0sproject.io/stack-checksum: sum-cr}
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: system-coredns
  labels: {k0s.k0sproject.io/stack: coredns}
  annotations: {k0s.k0sproject.io/stack-checksum: sum-crb}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: system-coredns}
subjects:
  - {kind: ServiceAccount, name: coredns, namespace: kube-system}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns
  namespace: kube-system
  labels: {k0s.k0sproject.io/stack: coredns}
  annotations: {k0s.k0sproject.io/stack-checksum: sum-cm}
data:
  Corefile: ".:53 {\n    forward . /etc/resolv.conf\n}\n"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: coredns
  namespace: kube-system
  labels: {k8s-app: kube-dns, k0s.k0sproject.io/stack: coredns}
  annotations: {k0s.k0sproject.io/stack-checksum: sum-deploy, k0s.k0sproject.io/last-applied-configuration: "{}"}
spec:
  replicas: 3
  strategy:
    type: RollingUpdate
    rollingUpdate: {maxUnavailable: 1, maxSurge: "25%"}
  selector:
    matchLabels: {k8s-app: kube-dns}
  template:
    metadata:
      labels: {k8s-app: kube-dns}
    spec:
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - topologyKey: kubernetes.io/hostname
              labelSelector:
                matchLabels: {k8s-app: kube-dns}
      topologySpreadConstraints:
        - topologyKey: topology.kubernetes.io/zone
          maxSkew: 1
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels: {k8s-app: kube-dns}
      containers:
        - name: coredns
          image: registry.k8s.io/coredns/coredns:v1.12.0
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: coredns
  namespace: kube-system
  labels: {k8s-app: kube-dns, k0s.k0sproject.io/stack: coredns}
  annotations: {k0s.k0sproject.io/stack-checksum: sum-pdb}
spec:
  minAvailable: 50%
  selector:
    matchLabels: {k8s-app: kube-dns}
---
apiVersion: v1
kind: Service
metadata:
  name: kube-dns
  namespace: kube-system
  labels: {k8s-app: kube-dns, k0s.k0sproject.io/stack: coredns}
  annotations: {k0s.k0sproject.io/stack-checksum: sum-svc}
spec:
  selector: {k8s-app: kube-dns}
  ports:
    - {name: dns, port: 53, protocol: UDP}
`

func createK0sCoreDNS(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	for _, obj := range decodeDocuments(t, []byte(k0sCoreDNSObjects)) {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatalf("create %s %s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
}

func releaseCoreDNSGroup(t *testing.T, dnsIP string) release.Group {
	t.Helper()
	bundle := release.Bundle{Groups: []release.Group{{Name: "coredns", Objects: corednsManifest(t)}}}
	vars := release.SettingVars(map[string]string{"dns.upstreams": "217.218.127.127 217.218.155.155"}, "10.0.10.10")
	vars[release.VarDNSIP] = dnsIP
	substituted, err := release.Substitute(bundle, vars)
	if err != nil {
		t.Fatal(err)
	}
	return substituted.Groups[0]
}

func liveObject(t *testing.T, c client.Client, kind, apiVersion, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	live := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind}}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, live); err != nil {
		t.Fatalf("get %s %s: %v", kind, name, err)
	}
	return live
}

func k0sStackLabelOf(obj *unstructured.Unstructured) string {
	return obj.GetLabels()["k0s.k0sproject.io/stack"]
}

func TestTakeoverReplacesTheK0sObjectsInPlace(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	ctx := context.Background()
	createK0sCoreDNS(t, c)
	service := liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	dnsIP, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	group := releaseCoreDNSGroup(t, dnsIP)
	uids := map[string]string{}
	for _, obj := range group.Objects {
		uids[obj.GetKind()+"/"+obj.GetName()] = string(liveObject(t, c, obj.GetKind(), obj.GetAPIVersion(), obj.GetNamespace(), obj.GetName()).GetUID())
	}

	if err := adoptK0sCoreDNS(c)(ctx, group); err != nil {
		t.Fatal(err)
	}

	for _, obj := range group.Objects {
		live := liveObject(t, c, obj.GetKind(), obj.GetAPIVersion(), obj.GetNamespace(), obj.GetName())
		if string(live.GetUID()) != uids[obj.GetKind()+"/"+obj.GetName()] {
			t.Errorf("%s %s was deleted and created again", obj.GetKind(), obj.GetName())
		}
		if k0sStackLabelOf(live) != "" {
			t.Errorf("%s %s keeps the k0s stack label, so k0s could prune it", obj.GetKind(), obj.GetName())
		}
		if live.GetAnnotations()["k0s.k0sproject.io/stack-checksum"] == "" {
			t.Errorf("%s %s lost the k0s checksum, so k0s could write its own version back", obj.GetKind(), obj.GetName())
		}
	}
	deployment := liveObject(t, c, "Deployment", "apps/v1", "kube-system", "coredns")
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if image := containers[0].(map[string]any)["image"]; image != "docker.io/coredns/coredns:1.13.2" {
		t.Errorf("image %v", image)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "template", "spec", "topologySpreadConstraints"); found {
		t.Error("a field of k0s that the release does not set must go")
	}
	service = liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	if ip, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP"); ip != dnsIP {
		t.Errorf("clusterIP %q, want %q", ip, dnsIP)
	}
	var configMap corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "coredns"}, &configMap); err != nil {
		t.Fatal(err)
	}
	if corefile := configMap.Data["Corefile"]; !strings.Contains(corefile, "forward . 217.218.127.127 217.218.155.155\n") {
		t.Errorf("corefile %q", corefile)
	}
}

func TestReleaseApplyAfterTheTakeoverKeepsTheObjects(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	ctx := context.Background()
	createK0sCoreDNS(t, c)
	service := liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	dnsIP, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	group := releaseCoreDNSGroup(t, dnsIP)
	if err := adoptK0sCoreDNS(c)(ctx, group); err != nil {
		t.Fatal(err)
	}
	before := string(liveObject(t, c, "Deployment", "apps/v1", "kube-system", "coredns").GetUID())
	if err := (release.Applier{Client: c}).Apply(ctx, group); err != nil {
		t.Fatal(err)
	}
	live := liveObject(t, c, "Deployment", "apps/v1", "kube-system", "coredns")
	if string(live.GetUID()) != before || k0sStackLabelOf(live) != "" {
		t.Fatalf("deployment %s label %q", live.GetUID(), k0sStackLabelOf(live))
	}
}

func TestTakeoverIgnoresOtherGroups(t *testing.T) {
	c, _ := StartTestEnv(t)
	createK0sCoreDNS(t, c)
	service := liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	dnsIP, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	group := releaseCoreDNSGroup(t, dnsIP)
	group.Name = "traefik"
	if err := adoptK0sCoreDNS(c)(context.Background(), group); err != nil {
		t.Fatal(err)
	}
	if k0sStackLabelOf(liveObject(t, c, "Deployment", "apps/v1", "kube-system", "coredns")) != "coredns" {
		t.Fatal("only the coredns group takes the objects over")
	}
}

func TestTakeoverLeavesObjectsOfAnotherOwner(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	ctx := context.Background()
	foreign := decodeDocuments(t, []byte(k0sCoreDNSObjects))
	for _, obj := range foreign {
		obj.SetLabels(map[string]string{"k8s-app": "kube-dns"})
		obj.SetAnnotations(nil)
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	service := liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	dnsIP, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	before := liveObject(t, c, "Deployment", "apps/v1", "kube-system", "coredns").GetResourceVersion()
	if err := adoptK0sCoreDNS(c)(ctx, releaseCoreDNSGroup(t, dnsIP)); err != nil {
		t.Fatal(err)
	}
	if after := liveObject(t, c, "Deployment", "apps/v1", "kube-system", "coredns").GetResourceVersion(); after != before {
		t.Fatal("an object that k0s does not own is not replaced")
	}
}

func TestTakeoverOnAClusterWithoutK0sCoreDNS(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	if err := adoptK0sCoreDNS(c)(context.Background(), releaseCoreDNSGroup(t, "10.0.0.10")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRunsTheTakeoverBeforeTheCoreDNSGroup(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	createK0sCoreDNS(t, c)
	service := liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	dnsIP, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	if err := beforeGroupHooks(c)(context.Background(), releaseCoreDNSGroup(t, dnsIP)); err != nil {
		t.Fatal(err)
	}
	if k0sStackLabelOf(liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")) != "" {
		t.Fatal("the hook of an install must take the k0s objects over")
	}
}

func createCluster(t *testing.T, c client.Client) {
	t.Helper()
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: "v0.3.0", API: v1alpha1.APISpec{VIP: "10.0.10.10"}}}
	if err := c.Create(context.Background(), cluster); err != nil {
		t.Fatal(err)
	}
}

func takeoverMark(t *testing.T, c client.Client) string {
	t.Helper()
	var cluster v1alpha1.Cluster
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	return cluster.Annotations[v1alpha1.AnnotationK0sCoreDNSTakeover]
}

func TestTakeoverMarksTheClusterWhenTheObjectsAreReplaced(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	createK0sCoreDNS(t, c)
	service := liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	dnsIP, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	if takeoverMark(t, c) != "" {
		t.Fatal("the mark must not exist before the take over")
	}
	if err := adoptK0sCoreDNS(c)(context.Background(), releaseCoreDNSGroup(t, dnsIP)); err != nil {
		t.Fatal(err)
	}
	if takeoverMark(t, c) != "done" {
		t.Fatalf("mark %q", takeoverMark(t, c))
	}
}

func TestTakeoverDoesNotMarkTheClusterForOtherGroups(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	group := releaseCoreDNSGroup(t, "10.0.0.10")
	group.Name = "traefik"
	if err := adoptK0sCoreDNS(c)(context.Background(), group); err != nil {
		t.Fatal(err)
	}
	if takeoverMark(t, c) != "" {
		t.Fatal("only the coredns group marks the take over")
	}
}

func TestTakeoverKeepsTheReplicasThatTheAddonOwns(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	createK0sCoreDNS(t, c)
	service := liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	dnsIP, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	if err := adoptK0sCoreDNS(c)(context.Background(), releaseCoreDNSGroup(t, dnsIP)); err != nil {
		t.Fatal(err)
	}
	deployment := liveObject(t, c, "Deployment", "apps/v1", "kube-system", "coredns")
	if replicas, _, _ := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "replicas"); fmt.Sprint(replicas) != "3" {
		t.Fatalf("replicas %v: a replace without them would drop the cluster to one", replicas)
	}
}

func TestTakeoverReplacesTheK0sDisruptionBudget(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	createK0sCoreDNS(t, c)
	service := liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	dnsIP, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	before := string(liveObject(t, c, "PodDisruptionBudget", "policy/v1", "kube-system", "coredns").GetUID())
	if err := adoptK0sCoreDNS(c)(context.Background(), releaseCoreDNSGroup(t, dnsIP)); err != nil {
		t.Fatal(err)
	}
	budget := liveObject(t, c, "PodDisruptionBudget", "policy/v1", "kube-system", "coredns")
	if string(budget.GetUID()) != before || k0sStackLabelOf(budget) != "" {
		t.Fatalf("budget %s label %q", budget.GetUID(), k0sStackLabelOf(budget))
	}
	if limit, _, _ := unstructured.NestedFieldNoCopy(budget.Object, "spec", "maxUnavailable"); fmt.Sprint(limit) != "1" {
		t.Fatalf("maxUnavailable %v", limit)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(budget.Object, "spec", "minAvailable"); found {
		t.Fatal("the k0s limit must go")
	}
}

func TestTakeoverSetsTheRollingUpdateThatKeepsAReadyPod(t *testing.T) {
	c, _ := StartTestEnv(t)
	createCluster(t, c)
	createK0sCoreDNS(t, c)
	service := liveObject(t, c, "Service", "v1", "kube-system", "kube-dns")
	dnsIP, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	if err := adoptK0sCoreDNS(c)(context.Background(), releaseCoreDNSGroup(t, dnsIP)); err != nil {
		t.Fatal(err)
	}
	deployment := liveObject(t, c, "Deployment", "apps/v1", "kube-system", "coredns")
	if unavailable, surge := rollingUpdateLimits(t, deployment); unavailable != "0" || surge != "1" {
		t.Fatalf("maxUnavailable %s maxSurge %s", unavailable, surge)
	}
	preferred, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "affinity", "podAntiAffinity", "preferredDuringSchedulingIgnoredDuringExecution")
	if len(preferred) != 1 {
		t.Fatalf("anti-affinity %v", deployment.Object["spec"])
	}
	if _, found, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution"); found {
		t.Fatal("the required rule of k0s must go")
	}
}
