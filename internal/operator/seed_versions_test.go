package operator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSeedDatabaseVersionsPassTheCRD(t *testing.T) {
	c, _ := StartTestEnv(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "addons", "cnpg", "seed-versions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var names []string
	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if err := c.Create(context.Background(), &obj, client.DryRunAll); err != nil {
			t.Fatalf("%s: %v", obj.GetName(), err)
		}
		names = append(names, obj.GetName())
	}
	want := []string{"postgresql-16", "postgresql-17", "postgresql-18"}
	if len(names) != len(want) {
		t.Fatalf("seed objects %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("seed objects %v", names)
		}
	}
}
