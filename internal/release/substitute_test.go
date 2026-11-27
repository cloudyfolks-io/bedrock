package release

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func objWithEnv(value string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "ovn-central"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "env": []any{map[string]any{"name": "NODE_IPS", "value": value}}}}}}},
	}}
}

func TestSubstituteReplacesPlaceholders(t *testing.T) {
	in := Bundle{Groups: []Group{{Name: "fabric", Objects: []*unstructured.Unstructured{objWithEnv("${BEDROCK_MASTER_IPS}"), objWithEnv("static")}}}}
	out, err := Substitute(in, map[string]string{VarMasterIPs: "10.0.10.11,10.0.10.12"})
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := unstructured.NestedSlice(out.Groups[0].Objects[0].Object, "spec", "template", "spec", "containers")
	env := got[0].(map[string]any)["env"].([]any)[0].(map[string]any)["value"]
	if env != "10.0.10.11,10.0.10.12" {
		t.Fatalf("env %v", env)
	}
	orig, _, _ := unstructured.NestedSlice(in.Groups[0].Objects[0].Object, "spec", "template", "spec", "containers")
	if orig[0].(map[string]any)["env"].([]any)[0].(map[string]any)["value"] != "${BEDROCK_MASTER_IPS}" {
		t.Fatal("input bundle must not be mutated")
	}
	if out.Groups[0].Objects[1] != in.Groups[0].Objects[1] {
		t.Fatal("objects without placeholders are shared")
	}
}

func TestSubstituteFailsOnUnknownPlaceholder(t *testing.T) {
	in := Bundle{Groups: []Group{{Name: "fabric", Objects: []*unstructured.Unstructured{objWithEnv("${BEDROCK_NOPE}")}}}}
	_, err := Substitute(in, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "BEDROCK_NOPE") || !strings.Contains(err.Error(), "ovn-central") {
		t.Fatalf("err %v", err)
	}
}

func objWithArgs(args ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "cert-manager"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "args": args}}}}},
	}}
}

func substitutedArgs(t *testing.T, vars map[string]string, args ...any) []any {
	t.Helper()
	in := Bundle{Groups: []Group{{Name: "cert-manager", Objects: []*unstructured.Unstructured{objWithArgs(args...)}}}}
	out, err := Substitute(in, vars)
	if err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(out.Groups[0].Objects[0].Object, "spec", "template", "spec", "containers")
	return containers[0].(map[string]any)["args"].([]any)
}

func TestSubstituteDropsAListItemThatResolvesToNothing(t *testing.T) {
	got := substitutedArgs(t, map[string]string{"BEDROCK_FLAG": ""}, "--v=2", "${BEDROCK_FLAG}", "--last")
	if len(got) != 2 || got[0] != "--v=2" || got[1] != "--last" {
		t.Fatalf("args %v", got)
	}
}

func TestSubstituteKeepsAListItemThatResolvesToText(t *testing.T) {
	got := substitutedArgs(t, map[string]string{"BEDROCK_FLAG": "--flag=1"}, "--v=2", "${BEDROCK_FLAG}")
	if len(got) != 2 || got[1] != "--flag=1" {
		t.Fatalf("args %v", got)
	}
}

func TestSubstituteKeepsAnEmptyListItemWithoutAPlaceholder(t *testing.T) {
	got := substitutedArgs(t, map[string]string{"BEDROCK_FLAG": "x"}, "", "${BEDROCK_FLAG}")
	if len(got) != 2 || got[0] != "" {
		t.Fatalf("args %v", got)
	}
}

func TestSubstituteKeepsAnEmptyMapValue(t *testing.T) {
	in := Bundle{Groups: []Group{{Name: "g", Objects: []*unstructured.Unstructured{objWithEnv("${BEDROCK_PROXY}")}}}}
	out, err := Substitute(in, map[string]string{"BEDROCK_PROXY": ""})
	if err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(out.Groups[0].Objects[0].Object, "spec", "template", "spec", "containers")
	env := containers[0].(map[string]any)["env"].([]any)
	if len(env) != 1 || env[0].(map[string]any)["value"] != "" {
		t.Fatalf("env %v", env)
	}
}
