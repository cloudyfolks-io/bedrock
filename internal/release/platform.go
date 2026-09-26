package release

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func writePlatform(path layout.Path, desc *remote.Descriptor, platform v1.Platform) (v1.Descriptor, error) {
	if desc.MediaType.IsIndex() {
		index, err := desc.ImageIndex()
		if err != nil {
			return v1.Descriptor{}, err
		}
		if err := writeIndexPlatform(path, index, platform); err != nil {
			return v1.Descriptor{}, err
		}
		return describe(index)
	}
	img, err := desc.Image()
	if err != nil {
		return v1.Descriptor{}, err
	}
	if err := requireImagePlatform(img, platform); err != nil {
		return v1.Descriptor{}, err
	}
	if err := path.WriteImage(img); err != nil {
		return v1.Descriptor{}, err
	}
	return describe(img)
}

func writeIndexPlatform(path layout.Path, index v1.ImageIndex, platform v1.Platform) error {
	manifest, err := index.IndexManifest()
	if err != nil {
		return err
	}
	child, err := platformManifest(manifest.Manifests, platform)
	if err != nil {
		return err
	}
	img, err := index.Image(child.Digest)
	if err != nil {
		return err
	}
	if err := path.WriteImage(img); err != nil {
		return err
	}
	digest, err := index.Digest()
	if err != nil {
		return err
	}
	raw, err := index.RawManifest()
	if err != nil {
		return err
	}
	return path.WriteBlob(digest, io.NopCloser(bytes.NewReader(raw)))
}

func platformManifest(manifests []v1.Descriptor, platform v1.Platform) (v1.Descriptor, error) {
	for _, manifest := range manifests {
		if manifest.MediaType.IsImage() && manifest.Platform != nil && platformMatches(*manifest.Platform, platform) {
			return manifest, nil
		}
	}
	if slices.ContainsFunc(manifests, func(manifest v1.Descriptor) bool { return manifest.MediaType.IsIndex() }) {
		return v1.Descriptor{}, fmt.Errorf("no %s manifest, and the index has a nested index, which a bundle does not support", platform)
	}
	return v1.Descriptor{}, fmt.Errorf("no %s manifest, the index has %s", platform, strings.Join(platformNames(manifests), ", "))
}

func platformNames(manifests []v1.Descriptor) []string {
	names := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		if manifest.Platform == nil {
			names = append(names, "no platform")
			continue
		}
		names = append(names, manifest.Platform.String())
	}
	return names
}

func requireImagePlatform(img v1.Image, platform v1.Platform) error {
	config, err := img.ConfigFile()
	if err != nil {
		return err
	}
	have := v1.Platform{OS: config.OS, Architecture: config.Architecture, Variant: config.Variant}
	if have.OS == "" || have.Architecture == "" || platformMatches(have, platform) {
		return nil
	}
	return fmt.Errorf("the image is %s, the bundle is for %s", have, platform)
}

func platformMatches(have, want v1.Platform) bool {
	return have.OS == want.OS && have.Architecture == want.Architecture && slices.Contains(acceptedVariants(want.Architecture), have.Variant)
}

func acceptedVariants(arch string) []string {
	if arch == "arm64" {
		return []string{"", "v8"}
	}
	return []string{""}
}

func describe(item partial.Describable) (v1.Descriptor, error) {
	desc, err := partial.Descriptor(item)
	if err != nil {
		return v1.Descriptor{}, err
	}
	return *desc, nil
}
