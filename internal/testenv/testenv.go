package testenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func CRDPaths() []string {
	root, err := moduleRoot()
	if err != nil {
		panic(err)
	}
	paths, err := crdPathsIn(root)
	if err != nil {
		panic(err)
	}
	return paths
}

func crdPathsIn(root string) ([]string, error) {
	vendors, err := os.ReadDir(filepath.Join(root, "test", "crds"))
	if err != nil {
		return nil, fmt.Errorf("read vendored CRDs: %w", err)
	}
	paths := []string{filepath.Join(root, "manifests", "00-crds")}
	for _, vendor := range vendors {
		if vendor.IsDir() {
			paths = append(paths, filepath.Join(root, "test", "crds", vendor.Name()))
		}
	}
	return paths, nil
}

func moduleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("locate the testenv source file")
	}
	return findModuleRoot(filepath.Dir(file))
}

func findModuleRoot(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return dir, nil
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		return "", errors.New("go.mod not found in any parent directory")
	}
	return findModuleRoot(parent)
}
