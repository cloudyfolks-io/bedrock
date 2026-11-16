package database

import (
	"context"
	"testing"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func TestResolveAndRenderManagedProxyPreserveTheTenantEndpoint(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(
		&v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{ManagementCIDR: "192.0.2.0/24"}},
		&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "network.tenant-management.enabled"}, Spec: v1alpha1.SettingSpec{Value: "true"}},
		&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "network.dns-servers"}, Spec: v1alpha1.SettingSpec{Value: "100.66.0.53"}},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "kata"}, Handler: "kata"},
	).Build()
	pods, refused, err := resolvePods(context.Background(), c, []string{haproxy}, goldenSubnet())
	if err != nil || refused != nil {
		t.Fatalf("refused=%v err=%v", refused, err)
	}
	if pods.NIC.Class != network.NetworkClassManaged || len(pods.NIC.ManagementRoutes) != 4 || pods.NIC.ManagementRoutes[0].Destination != "192.0.2.0/24" || pods.RuntimeClass != "kata" {
		t.Fatalf("pods=%+v", pods)
	}
	in := goldenProxy()
	in.NIC = pods.NIC
	objects, err := ProxyObjects(in)
	if err != nil {
		t.Fatal(err)
	}
	deployment := objectOfKind(objects, "Deployment")
	annotations, _, _ := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "annotations")
	labels, _, _ := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
	if annotations["k8s.v1.cni.cncf.io/networks"] != "kube-system/vpc" || annotations["fabric.cloudyfolks.io/logical_switch"] != network.TenantManagedSubnet || labels[network.NetworkClassLabel] != network.NetworkClassManaged {
		t.Fatalf("annotations=%v labels=%v", annotations, labels)
	}
	if pinnedByProxy(deployment) != in.Address || annotations["vpc.kube-system.fabric.cloudyfolks.io/security_groups"] != network.FabricName(in.Database.Namespace, SecurityGroupName(in.Database.UID)) {
		t.Fatalf("the endpoint address or its security groups changed: %v", annotations)
	}
}

func TestDatabaseWatchesNetworkAndRuntimeSettings(t *testing.T) {
	for _, name := range []string{"database.enabled", "network.tenant-management.enabled", "network.tenant-management.managed-cidr", "container.runtime-class"} {
		if !databaseSetting(&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: name}}) {
			t.Fatalf("the database ignores %s", name)
		}
	}
	if databaseSetting(&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "registry.host"}}) {
		t.Fatal("an unrelated setting selects databases")
	}
}
