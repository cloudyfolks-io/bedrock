package operator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestSeedDatabaseVersionsPassTheCRD(t *testing.T) {
	c, _ := StartTestEnv(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "addons", "cnpg", "seed-versions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	names := seedNames(t, c, raw)
	want := []string{"postgresql-16", "postgresql-17", "postgresql-18"}
	if !slices.Equal(names, want) {
		t.Fatalf("seed objects %v", names)
	}
}

func TestPerconaSeedVersionsPassTheCRD(t *testing.T) {
	c, _ := StartTestEnv(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "addons", "percona-ps", "seed-versions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	names := seedNames(t, c, raw)
	want := []string{"mysql-8.4", "mysql-9.7"}
	if !slices.Equal(names, want) {
		t.Fatalf("seed objects %v", names)
	}
	for _, name := range want {
		var version v1alpha1.DatabaseVersion
		decoded := decodeSeed(t, raw, name)
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(decoded.Object, &version); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"backup", "haproxy", "init", "toolkit"} {
			if !strings.Contains(version.Spec.Companions[key], "@sha256:") {
				t.Errorf("%s: companion %q is not pinned: %q", name, key, version.Spec.Companions[key])
			}
		}
	}
}

func TestValkeySeedVersionsPassTheCRD(t *testing.T) {
	c, _ := StartTestEnv(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "database-valkey-versions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	names := seedNames(t, c, raw)
	want := []string{"valkey-8.1", "valkey-9.0", "valkey-9.1"}
	if !slices.Equal(names, want) {
		t.Fatalf("seed objects %v", names)
	}
	for _, name := range want {
		var version v1alpha1.DatabaseVersion
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(decodeSeed(t, raw, name).Object, &version); err != nil {
			t.Fatal(err)
		}
		if version.Spec.Port != 6379 || !strings.Contains(version.Spec.Companions["exporter"], "@sha256:") {
			t.Errorf("%s: %+v", name, version.Spec)
		}
	}
}

func decodeSeed(t *testing.T, raw []byte, name string) *unstructured.Unstructured {
	t.Helper()
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if obj.GetName() == name {
			return &obj
		}
	}
}

func seedNames(t *testing.T, c client.Client, raw []byte) []string {
	t.Helper()
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
	return names
}
