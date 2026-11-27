package release

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

const frrImage = "quay.io/frrouting/frr:10.7.0"

func TestFabricRendersTheFRRAgentOnGatewayNodes(t *testing.T) {
	bundle, _ := buildReal(t, "fabric")
	agent := objectNamed(groupObjects(bundle, "fabric"), "DaemonSet", "fabric-frr")
	if agent == nil {
		t.Fatal("missing DaemonSet fabric-frr")
	}
	if agent.GetNamespace() != "kube-system" {
		t.Errorf("namespace %q", agent.GetNamespace())
	}
	selector, _, _ := unstructured.NestedStringMap(agent.Object, "spec", "template", "spec", "nodeSelector")
	if selector[roles.FabricGWLabel] != "true" {
		t.Errorf("nodeSelector %v", selector)
	}
}

func TestFabricFRRImageIsInTheImageList(t *testing.T) {
	bundle, out := buildReal(t, "fabric")
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), frrImage) || !slices.Contains(bundle.Images, frrImage) || !slices.Contains(ImagesOf(bundle.Groups), frrImage) {
		t.Fatalf("images %v", bundle.Images)
	}
}

func fabricControllerArgs(t *testing.T) []any {
	t.Helper()
	bundle, _ := buildReal(t, "fabric")
	controller := objectNamed(groupObjects(bundle, "fabric"), "Deployment", "fabric-controller")
	if controller == nil {
		t.Fatal("missing Deployment fabric-controller")
	}
	containers, _, _ := unstructured.NestedSlice(controller.Object, "spec", "template", "spec", "containers")
	args, _, _ := unstructured.NestedSlice(containers[0].(map[string]any), "args")
	return args
}

func TestFabricNetworkPolicyEnforcementComesFromTheSetting(t *testing.T) {
	if !slices.Contains(fabricControllerArgs(t), any(`--np-enforcement="${BEDROCK_NP_ENFORCEMENT}"`)) {
		t.Fatalf("args %v", fabricControllerArgs(t))
	}
}

func TestFabricHandlesClaimedLoadBalancerServices(t *testing.T) {
	args := fabricControllerArgs(t)
	if !slices.Contains(args, any("--enable-ovn-lb-svc=true")) {
		t.Fatalf("args %v", args)
	}
}

func TestFabricTLSComesFromTheSettingAndTheBundleHoldsNoKey(t *testing.T) {
	bundle, _ := buildReal(t, "fabric")
	objects := groupObjects(bundle, "fabric")
	if secret := objectNamed(objects, "Secret", "fabric-tls"); secret != nil {
		t.Fatal("the bundle must not carry a private key: the operator makes the Secret fabric-tls")
	}
	monitor := objectNamed(objects, "Deployment", "fabric-monitor")
	if monitor == nil {
		t.Fatal("missing Deployment fabric-monitor")
	}
	containers, _, _ := unstructured.NestedSlice(monitor.Object, "spec", "template", "spec", "containers")
	env, _, _ := unstructured.NestedSlice(containers[0].(map[string]any), "env")
	for _, item := range env {
		entry := item.(map[string]any)
		if entry["name"] == "ENABLE_SSL" {
			if entry["value"] != "${BEDROCK_FABRIC_ENABLE_SSL}" {
				t.Fatalf("ENABLE_SSL = %v", entry["value"])
			}
			return
		}
	}
	t.Fatal("ENABLE_SSL not found in the monitor")
}
