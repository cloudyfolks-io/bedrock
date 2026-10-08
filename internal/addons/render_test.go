package addons

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func lokiVarsFor(t *testing.T, days string) map[string]string {
	t.Helper()
	def, _ := Lookup(Loki)
	values, err := ResolveValues(def, nil)
	if err != nil {
		t.Fatal(err)
	}
	vars, err := def.Vars(values, map[string]string{"loki.retention-days": days})
	if err != nil {
		t.Fatal(err)
	}
	return vars
}

func TestRenderSubstitutesAndLabels(t *testing.T) {
	rendered, err := Render(Loki, lokiObjects(t), lokiVarsFor(t, "14"))
	if err != nil {
		t.Fatal(err)
	}
	statefulSet := find(t, rendered, "StatefulSet", "loki")
	claims, _, _ := unstructured.NestedSlice(statefulSet.Object, "spec", "volumeClaimTemplates")
	spec := claims[0].(map[string]any)["spec"].(map[string]any)
	if spec["storageClassName"] != "block" || spec["resources"].(map[string]any)["requests"].(map[string]any)["storage"] != "20Gi" {
		t.Errorf("claim %v", spec)
	}
	for _, obj := range rendered {
		labels := obj.GetLabels()
		if labels["bedrock.cloudyfolks.io/kind"] != "Addon" || labels["bedrock.cloudyfolks.io/name"] != "loki" {
			t.Errorf("%s %s labels %v", obj.GetKind(), obj.GetName(), labels)
		}
	}
}

func TestRenderDoesNotMutateTheBundle(t *testing.T) {
	objects := lokiObjects(t)
	before := make([]map[string]any, len(objects))
	for i, obj := range objects {
		before[i] = obj.DeepCopy().Object
	}
	if _, err := Render(Loki, objects, lokiVarsFor(t, "14")); err != nil {
		t.Fatal(err)
	}
	for i, obj := range objects {
		if !reflect.DeepEqual(before[i], obj.Object) {
			t.Errorf("%s %s changed", obj.GetKind(), obj.GetName())
		}
	}
}

func TestRenderFailsOnAnUnresolvedPlaceholder(t *testing.T) {
	if _, err := Render(Loki, lokiObjects(t), map[string]string{}); err == nil {
		t.Fatal("expected an unresolved placeholder error")
	}
}

func templateAnnotation(t *testing.T, objects []*unstructured.Unstructured, kind, name string) string {
	t.Helper()
	obj := find(t, objects, kind, name)
	value, _, _ := unstructured.NestedString(obj.Object, "spec", "template", "metadata", "annotations", AnnotationConfigHash)
	return value
}

func TestRenderAddsTheConfigHash(t *testing.T) {
	rendered, err := Render(Loki, lokiObjects(t), lokiVarsFor(t, "14"))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range [][2]string{{"StatefulSet", "loki"}, {"DaemonSet", "alloy"}} {
		if templateAnnotation(t, rendered, target[0], target[1]) == "" {
			t.Errorf("%s %s has no config hash", target[0], target[1])
		}
	}
}

func TestConfigHashChangesWithRetention(t *testing.T) {
	objects := lokiObjects(t)
	long, err := Render(Loki, objects, lokiVarsFor(t, "14"))
	if err != nil {
		t.Fatal(err)
	}
	short, err := Render(Loki, objects, lokiVarsFor(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	if templateAnnotation(t, long, "StatefulSet", "loki") == templateAnnotation(t, short, "StatefulSet", "loki") {
		t.Error("the Loki hash ignores the retention")
	}
	if templateAnnotation(t, long, "DaemonSet", "alloy") != templateAnnotation(t, short, "DaemonSet", "alloy") {
		t.Error("the Alloy hash follows the retention")
	}
}
