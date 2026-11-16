package clusters

import (
	"testing"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func TestResolveManagedControlPlaneUsesItsTenantAddressAndConfiguredRuntime(t *testing.T) {
	for _, runtime := range []string{"kata", "none", "missing"} {
		t.Run(runtime, func(t *testing.T) {
			objects := []client.Object{
				&v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{ManagementCIDR: "192.0.2.0/24"}},
				&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "network.tenant-management.enabled"}, Spec: v1alpha1.SettingSpec{Value: "true"}},
				&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "container.runtime-class"}, Spec: v1alpha1.SettingSpec{Value: runtime}},
				&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "kata"}, Handler: "kata"},
			}
			h := newHarness(t, objects...)
			resolved, gate := resolveCluster(t, h, testCluster2("prod"))
			if runtime != "kata" {
				requireGate(t, gate, ReasonRuntimeUnavailable)
				return
			}
			if gate != nil || resolved.NIC.Class != network.NetworkClassManaged || resolved.RuntimeClass != "kata" || resolved.Address != "10.100.0.10" || resolved.NIC.Address != resolved.Address {
				t.Fatalf("resolved=%+v gate=%+v", resolved, gate)
			}
			if len(resolved.NIC.ManagementRoutes) != 4 || resolved.NIC.ManagementRoutes[0].Destination != "192.0.2.0/24" {
				t.Fatalf("routes=%v", resolved.NIC.ManagementRoutes)
			}
		})
	}
}

func TestRenderManagedControlPlaneKeepsTheVPCAddressAndSetsBothNetworks(t *testing.T) {
	in := ControlPlaneInput{
		Cluster: testCluster(), Labels: testLabels, Address: "10.100.0.10", RuntimeClass: "kata",
		NIC: network.NIC{Class: network.NetworkClassManaged, ManagementRoutes: []network.PodRoute{{Destination: "192.0.2.0/24", Gateway: "100.89.0.1"}}},
	}
	obj, err := RenderControlPlane(in)
	if err != nil {
		t.Fatal(err)
	}
	patches := nested[[]any](t, obj, "spec", "patches")
	content := patches[0].(map[string]any)["patch"].(map[string]any)["content"].(string)
	var patch map[string]any
	if err := sigyaml.Unmarshal([]byte(content), &patch); err != nil {
		t.Fatal(err)
	}
	template := patch["spec"].(map[string]any)["template"].(map[string]any)
	metadata := template["metadata"].(map[string]any)
	annotations := metadata["annotations"].(map[string]any)
	labels := metadata["labels"].(map[string]any)
	if annotations["k8s.v1.cni.cncf.io/networks"] != "kube-system/vpc" || annotations["fabric.cloudyfolks.io/logical_switch"] != network.TenantManagedSubnet || labels[network.NetworkClassLabel] != network.NetworkClassManaged {
		t.Fatalf("metadata=%v", metadata)
	}
	if annotations["vpc.kube-system.fabric.cloudyfolks.io/ip_address"] != in.Address || template["spec"].(map[string]any)["runtimeClassName"] != "kata" {
		t.Fatalf("template=%v", template)
	}
	if nested[string](t, obj, "spec", "externalAddress") != in.Address {
		t.Fatal("the control plane address changed to its management address")
	}
}
