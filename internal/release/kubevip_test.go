package release

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestKubeVIPDaemonKeepsTheAddressThroughSlowLeaseRenewals(t *testing.T) {
	objects, err := loadObjects(os.DirFS(filepath.Join("..", "..")), "manifests/10-kube-vip", "kube-vip")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"vip_leaseduration": "15", "vip_renewdeadline": "10", "vip_retryperiod": "2"}
	for _, obj := range objects {
		if obj.GetKind() != "DaemonSet" || obj.GetName() != "kube-vip" {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		for _, item := range containers {
			container, _ := item.(map[string]any)
			if container["name"] != "kube-vip" {
				continue
			}
			got := map[string]string{}
			env, _ := container["env"].([]any)
			for _, entry := range env {
				pair, _ := entry.(map[string]any)
				name, _ := pair["name"].(string)
				value, _ := pair["value"].(string)
				got[name] = value
			}
			for name, value := range want {
				if got[name] != value {
					t.Fatalf("env %s is %q, want %q; a slow lease write must not move the address", name, got[name], value)
				}
			}
			return
		}
	}
	t.Fatal("container kube-vip of DaemonSet kube-vip not found")
}
