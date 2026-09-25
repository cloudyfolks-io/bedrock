package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const containerdDropInDir = "etc/k0s/containerd.d"

func MirrorFilesFor(mirrors []v1alpha1.MirrorSpec) map[string]string {
	if len(mirrors) == 0 {
		return map[string]string{}
	}
	files := map[string]string{"cri-registry.toml": "[plugins.\"io.containerd.cri.v1.images\".registry]\nconfig_path = \"/etc/k0s/containerd.d/certs.d\"\n"}
	for _, mirror := range mirrors {
		files["certs.d/"+mirror.Registry+"/hosts.toml"] = fmt.Sprintf("[host.%q]\ncapabilities = [\"pull\", \"resolve\"]\n", mirror.Endpoint)
	}
	return files
}

func MirrorFiles(mirror string) map[string]string {
	return MirrorFilesFor([]v1alpha1.MirrorSpec{{Registry: "_default", Endpoint: mirror}})
}

func EnsureMirrors(root string, mirrors []v1alpha1.MirrorSpec) error {
	dir := filepath.Join(root, containerdDropInDir)
	if err := writeFiles(dir, MirrorFilesFor(mirrors)); err != nil {
		return err
	}
	return removeStaleRegistries(filepath.Join(dir, "certs.d"), registriesOf(mirrors))
}

func EnsureMirror(root, mirror string) error {
	return EnsureMirrors(root, []v1alpha1.MirrorSpec{{Registry: "_default", Endpoint: mirror}})
}

func writeFiles(dir string, files map[string]string) error {
	for rel, content := range files {
		if _, err := ReplaceFile(filepath.Join(dir, rel), content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func registriesOf(mirrors []v1alpha1.MirrorSpec) map[string]struct{} {
	registries := make(map[string]struct{}, len(mirrors))
	for _, mirror := range mirrors {
		registries[mirror.Registry] = struct{}{}
	}
	return registries
}

func removeStaleRegistries(dir string, keep map[string]struct{}) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, wanted := keep[entry.Name()]; wanted || !entry.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
