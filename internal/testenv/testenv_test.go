package testenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	sigyaml "sigs.k8s.io/yaml"
)

func TestCRDPathsOfATree(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"test/crds/zeta", "test/crds/alpha", "manifests/00-crds"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "test", "crds", "README"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := crdPathsIn(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "manifests", "00-crds"), filepath.Join(root, "test", "crds", "alpha"), filepath.Join(root, "test", "crds", "zeta")}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths %v", got)
	}
}

func TestCRDPathsMissingVendorTree(t *testing.T) {
	if _, err := crdPathsIn(t.TempDir()); err == nil {
		t.Fatal("a tree without test/crds must fail")
	}
}

func TestFindModuleRoot(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := findModuleRoot(deep)
	if err != nil || got != root {
		t.Fatalf("root %q %v", got, err)
	}
}

func TestEveryVendorDirectoryHoldsCRDs(t *testing.T) {
	paths := CRDPaths()
	if len(paths) < 9 {
		t.Fatalf("want the generated CRDs and eight vendors, got %v", paths)
	}
	for _, path := range paths {
		files, err := filepath.Glob(filepath.Join(path, "*.yaml"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s holds no CRD files: %v", path, err)
		}
	}
}

func vendoredCRDNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, path := range CRDPaths() {
		files, err := filepath.Glob(filepath.Join(path, "*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var crd apiextensionsv1.CustomResourceDefinition
			if err := sigyaml.Unmarshal(body, &crd); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			names = append(names, crd.Name)
		}
	}
	return names
}

func TestEnvtestInstallsEveryCRD(t *testing.T) {
	env := &envtest.Environment{CRDDirectoryPaths: CRDPaths(), ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	var crds apiextensionsv1.CustomResourceDefinitionList
	if err := c.List(context.Background(), &crds); err != nil {
		t.Fatal(err)
	}
	installed := map[string]bool{}
	for _, crd := range crds.Items {
		installed[crd.Name] = true
	}
	for _, name := range vendoredCRDNames(t) {
		if !installed[name] {
			t.Errorf("CRD %s is not installed", name)
		}
	}
	for _, name := range []string{
		"virtualmachines.kubevirt.io", "datavolumes.cdi.kubevirt.io", "vpcs.fabric.cloudyfolks.io",
		"cephobjectstores.ceph.rook.io", "objectbucketclaims.objectbucket.io", "volumesnapshots.snapshot.storage.k8s.io",
		"backups.velero.io", "applicationawareresourcequotas.aaq.kubevirt.io", "network-attachment-definitions.k8s.cni.cncf.io",
	} {
		if !installed[name] {
			t.Errorf("CRD %s is not installed", name)
		}
	}
}
