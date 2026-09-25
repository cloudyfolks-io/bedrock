package ssa

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var deprecatedApplyPatch = regexp.MustCompile(`client\.Apply[,)]`)

func TestNoDeprecatedApplyPatch(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == "vendor" || entry.Name() == "dist" || strings.HasPrefix(entry.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if deprecatedApplyPatch.MatchString(line) {
				offenders = append(offenders, filepath.ToSlash(path)+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("the deprecated client.Apply patch type is still used at:\n%s", strings.Join(offenders, "\n"))
	}
}

func TestUnstructuredOfTyped(t *testing.T) {
	cm := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "cm"}, Data: map[string]string{"k": "v"}}
	u, err := unstructuredOf(cm)
	if err != nil {
		t.Fatal(err)
	}
	if u.GetKind() != "ConfigMap" || u.GetNamespace() != "ns" || u.GetName() != "cm" {
		t.Fatalf("identity lost: %v", u.Object)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(u.Object, "metadata", "creationTimestamp"); found {
		t.Fatal("creationTimestamp must be dropped")
	}
}

func TestUnstructuredOfCopiesUnstructured(t *testing.T) {
	in := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "cm"}}}
	out, err := unstructuredOf(in)
	if err != nil {
		t.Fatal(err)
	}
	out.SetName("changed")
	if in.GetName() != "cm" {
		t.Fatal("unstructuredOf must not share the input object")
	}
}

func TestStatusBodyDropsSpec(t *testing.T) {
	in := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "cm"}, "spec": map[string]any{"a": "b"}, "status": map[string]any{"c": "d"}}}
	out, err := statusBody(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := out.Object["spec"]; found {
		t.Fatal("a status apply must not carry spec")
	}
	if _, found := in.Object["spec"]; !found {
		t.Fatal("statusBody must not change its input")
	}
}
