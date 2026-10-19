package postgres

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (run with -update to create it): %v", path, err)
	}
	if string(want) != string(got) {
		t.Fatalf("%s differs from the golden file.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func objectsJSON(t *testing.T, objs []*unstructured.Unstructured) []byte {
	t.Helper()
	list := make([]map[string]any, 0, len(objs))
	for _, obj := range objs {
		list = append(list, obj.Object)
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func goldenObjects(t *testing.T, name string, objs []*unstructured.Unstructured) {
	t.Helper()
	golden(t, name, objectsJSON(t, objs))
}
