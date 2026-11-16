package network

import (
	"context"
	"encoding/json"
	"net/netip"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestResolvePodNICUsesActualClusterRoutesAndConfiguredGateways(t *testing.T) {
	scheme, err := testScheme()
	if err != nil {
		t.Fatal(err)
	}
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{ManagementCIDR: "192.0.2.0/24"}}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	cfg := enabledConfig(t)
	cfg.TenantManagement.WorkloadCIDR = netip.MustParsePrefix("100.88.0.0/16")
	cfg.TenantManagement.ManagedCIDR = netip.MustParsePrefix("100.89.0.0/16")
	for _, class := range []string{NetworkClassWorkload, NetworkClassManaged} {
		in := NIC{Class: class, Namespace: "acme-web", Subnet: "default", Address: "10.0.0.7", SecurityGroups: []string{"web"}}
		nic, err := ResolvePodNIC(context.Background(), reader, cfg, in)
		if err != nil {
			t.Fatal(err)
		}
		gateway, subnet, count := "100.88.0.1", TenantWorkloadSubnet, 3
		if class == NetworkClassManaged {
			gateway, subnet, count = "100.89.0.1", TenantManagedSubnet, 4
		}
		if len(nic.ManagementRoutes) != count || nic.ManagementRoutes[0] != (PodRoute{Destination: "192.0.2.0/24", Gateway: gateway}) {
			t.Fatalf("class %s routes %v", class, nic.ManagementRoutes)
		}
		meta := PodAttachment(nic)
		prefix := vpcAttachmentProvider + providerAnnotationDomain
		for key, want := range map[string]string{
			"fabric.cloudyfolks.io/logical_switch": subnet,
			"fabric.cloudyfolks.io/default_route":  "false",
			prefix + "default_route":               "true",
			prefix + "logical_switch":              FabricName(in.Namespace, in.Subnet),
			prefix + "ip_address":                  in.Address,
			prefix + "security_groups":             FabricName(in.Namespace, "web"),
		} {
			if meta.Annotations[key] != want {
				t.Errorf("%s = %q, want %q", key, meta.Annotations[key], want)
			}
		}
		var routes []PodRoute
		if err := json.Unmarshal([]byte(meta.Annotations["fabric.cloudyfolks.io/routes"]), &routes); err != nil || !reflect.DeepEqual(routes, nic.ManagementRoutes) {
			t.Fatalf("rendered routes %v err %v", routes, err)
		}
		if _, exists := meta.Annotations[defaultNetworkAnnotation]; exists || meta.Labels[NetworkClassLabel] != class {
			t.Fatalf("metadata %+v", meta)
		}
		if in.Class != class || len(in.ManagementRoutes) != 0 {
			t.Fatal("the input NIC changed")
		}
	}
}

func TestResolvePodNICPreservesTheDisabledNetworkWithoutReadingTheCluster(t *testing.T) {
	in := NIC{Class: NetworkClassWorkload, Namespace: "acme-web", Subnet: "default", Address: "10.0.0.7"}
	nic, err := ResolvePodNIC(context.Background(), nil, Config{}, in)
	if err != nil || nic.Class != "" || nic.Address != in.Address || len(nic.ManagementRoutes) != 0 {
		t.Fatalf("nic %+v err %v", nic, err)
	}
	if got := PodAttachment(nic).Annotations[defaultNetworkAnnotation]; got != "acme-web/default" {
		t.Fatalf("default network %q", got)
	}
}

func TestResolvePodNICRefusesAnEnabledNetworkWithoutClusterConfiguration(t *testing.T) {
	scheme, err := testScheme()
	if err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	if _, err := ResolvePodNIC(context.Background(), reader, enabledConfig(t), NIC{Class: NetworkClassWorkload}); err == nil {
		t.Fatal("the enabled network needs the actual cluster configuration")
	}
}
