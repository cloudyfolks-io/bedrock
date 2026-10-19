package release

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func cniServerArgs(t *testing.T, objects []*unstructured.Unstructured) []string {
	t.Helper()
	for _, obj := range objects {
		if obj.GetKind() != "DaemonSet" {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		for _, container := range containers {
			fields := container.(map[string]any)
			if fields["name"] == "cni-server" {
				args, _, _ := unstructured.NestedStringSlice(fields, "args")
				return args
			}
		}
	}
	t.Fatal("no cni-server container")
	return nil
}

func fabricGroupOf(objects ...*unstructured.Unstructured) Bundle {
	return Bundle{Groups: []Group{{Name: "fabric", Objects: objects}}}
}

func cniDaemonSet(args ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "DaemonSet",
		"metadata": map[string]any{"name": "fabric-cni", "namespace": "kube-system"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "cni-server", "args": args}},
		}}},
	}}
}

func TestPodMTUUnsetKeepsTheFabricRenderByteIdentical(t *testing.T) {
	bundle, _ := buildReal(t, "fabric")
	before, err := json.Marshal(bundle.Groups)
	if err != nil {
		t.Fatal(err)
	}
	for _, vars := range []map[string]string{{}, {VarPodMTU: ""}} {
		out, err := SetPodMTU(bundle, vars)
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(out.Groups)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("an empty pod MTU must leave the render unchanged")
		}
	}
	if slices.ContainsFunc(cniServerArgs(t, groupObjects(bundle, "fabric")), func(arg string) bool { return strings.HasPrefix(arg, "--mtu=") }) {
		t.Fatal("the release render must carry no mtu flag, so Fabric detects it from the NIC")
	}
}

func TestPodMTUMatchesTheFabricChartKey(t *testing.T) {
	bundle, _ := buildReal(t, "fabric")
	out, err := SetPodMTU(bundle, map[string]string{VarPodMTU: "1500"})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..")
	raw, err := exec.Command("helm", "template", "fabric", filepath.Join(root, "charts", "fabric"), "--namespace", "kube-system",
		"-f", filepath.Join(root, "release", "values", "fabric.yaml"), "--set", "networking.pods.mtu=1500").Output()
	if err != nil {
		t.Fatal(err)
	}
	docs, err := splitDocuments(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := cniServerArgs(t, docs)
	if !slices.Contains(want, "--mtu=1500") {
		t.Fatalf("the chart key networking.pods.mtu must render --mtu: %v", want)
	}
	if got := cniServerArgs(t, groupObjects(out, "fabric")); !slices.Equal(got, want) {
		t.Fatalf("args differ from the chart render\n got %v\nwant %v", got, want)
	}
}

func TestPodMTUDoesNotChangeTheInputBundle(t *testing.T) {
	in := fabricGroupOf(cniDaemonSet("--service-cluster-ip-range=10.96.0.0/12", "--iface=eth0"))
	out, err := SetPodMTU(in, map[string]string{VarPodMTU: "1400"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cniServerArgs(t, in.Groups[0].Objects); len(got) != 2 {
		t.Fatalf("input changed: %v", got)
	}
	want := []string{"--service-cluster-ip-range=10.96.0.0/12", "--mtu=1400", "--iface=eth0"}
	if got := cniServerArgs(t, out.Groups[0].Objects); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestPodMTUReplacesAnExistingFlag(t *testing.T) {
	in := fabricGroupOf(cniDaemonSet("--service-cluster-ip-range=10.96.0.0/12", "--mtu=1200", "--iface=eth0"))
	out, err := SetPodMTU(in, map[string]string{VarPodMTU: "1400"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--service-cluster-ip-range=10.96.0.0/12", "--mtu=1400", "--iface=eth0"}
	if got := cniServerArgs(t, out.Groups[0].Objects); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestPodMTUFailsWhenTheFabricAgentHasNoAnchorArgument(t *testing.T) {
	in := fabricGroupOf(cniDaemonSet("--iface=eth0"))
	if _, err := SetPodMTU(in, map[string]string{VarPodMTU: "1400"}); err == nil {
		t.Fatal("a Fabric chart that moved the argument must fail the install, not drop the MTU")
	}
}

func TestPodMTUFailsWhenThereIsNoFabricAgent(t *testing.T) {
	if _, err := SetPodMTU(Bundle{Groups: []Group{{Name: "fabric"}}}, map[string]string{VarPodMTU: "1400"}); err == nil {
		t.Fatal("a set pod MTU without a Fabric agent must fail")
	}
}

func TestPodMTUOnABundleWithoutFabricIsIgnoredWhenUnset(t *testing.T) {
	in := Bundle{Groups: []Group{{Name: "rook"}}}
	if _, err := SetPodMTU(in, map[string]string{}); err != nil {
		t.Fatal(err)
	}
}

func TestWithPodMTU(t *testing.T) {
	in := map[string]string{VarVIP: "10.0.0.10"}
	if out := WithPodMTU(in, 0); len(out) != 1 {
		t.Fatalf("zero must add nothing: %v", out)
	}
	out := WithPodMTU(in, 1500)
	if out[VarPodMTU] != "1500" || out[VarVIP] != "10.0.0.10" {
		t.Fatalf("got %v", out)
	}
	if _, ok := in[VarPodMTU]; ok {
		t.Fatal("input map changed")
	}
}

func TestInstallGroupsHandsTheFabricAgentThePodMTU(t *testing.T) {
	c, _ := startTestEnv(t)
	in := fabricGroupOf(cniDaemonSet("--service-cluster-ip-range=10.96.0.0/12"))
	var seen []string
	capture := func(_ context.Context, group Group) error {
		seen = cniServerArgs(t, group.Objects)
		return fmt.Errorf("stop")
	}
	vars := map[string]string{VarPodMTU: "1500"}
	_ = InstallGroups(context.Background(), c, in, vars, Gates{}, time.Second, time.Minute, func(Group, error) {}, capture, SkipHook)
	if !slices.Equal(seen, []string{"--service-cluster-ip-range=10.96.0.0/12", "--mtu=1500"}) {
		t.Fatalf("args %v", seen)
	}
}

func TestInstallGroupsRefusesAPodMTUWithoutAFabricAgent(t *testing.T) {
	c, _ := startTestEnv(t)
	in := Bundle{Groups: []Group{{Name: "fabric"}}}
	err := InstallGroups(context.Background(), c, in, map[string]string{VarPodMTU: "1500"}, Gates{}, time.Second, time.Minute, func(Group, error) {}, SkipHook, SkipHook)
	if err == nil {
		t.Fatal("expected an error")
	}
}
