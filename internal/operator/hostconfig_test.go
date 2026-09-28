package operator

import (
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestRenderHostConfigBaseline(t *testing.T) {
	spec := RenderHostConfig(v1alpha1.Host{}, v1alpha1.Cluster{})
	if len(spec.Modules) != 4 || spec.Sysctls["net.ipv4.ip_forward"] != "1" || len(spec.ContainerdMirrors) != 0 || len(spec.Packages) != 0 {
		t.Fatalf("spec %+v", spec)
	}
}

func TestRenderHostConfigMirror(t *testing.T) {
	cluster := v1alpha1.Cluster{}
	cluster.Spec.Registry.Mirror = "https://m.example"
	spec := RenderHostConfig(v1alpha1.Host{}, cluster)
	if len(spec.ContainerdMirrors) != 1 || spec.ContainerdMirrors[0].Registry != "_default" || spec.ContainerdMirrors[0].Endpoint != "https://m.example" {
		t.Fatalf("mirrors %+v", spec.ContainerdMirrors)
	}
}

func TestRenderHostConfigIsPure(t *testing.T) {
	a := RenderHostConfig(v1alpha1.Host{}, v1alpha1.Cluster{})
	a.Sysctls["x"] = "1"
	a.Modules[0] = "changed"
	b := RenderHostConfig(v1alpha1.Host{}, v1alpha1.Cluster{})
	if _, ok := b.Sysctls["x"]; ok {
		t.Fatal("render must not share maps between calls")
	}
	if b.Modules[0] == "changed" {
		t.Fatal("render must not share slices between calls")
	}
}
