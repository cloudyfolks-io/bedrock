package valkey

import (
	"context"
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

type fakeRunner struct {
	scripts []string
	outputs []string
	err     error
}

func (r *fakeRunner) Run(_ context.Context, stdin string) (string, error) {
	r.scripts = append(r.scripts, stdin)
	if r.err != nil {
		return "", r.err
	}
	if len(r.outputs) >= len(r.scripts) {
		return r.outputs[len(r.scripts)-1], nil
	}
	return "", nil
}

func readDadehat(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "dadehat", name+".golden"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
