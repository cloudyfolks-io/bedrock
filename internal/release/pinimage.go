package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func WithRunningImage(bundle Bundle, running string) (Bundle, error) {
	image := bundle.Spec.Image
	if running == image {
		return bundle, nil
	}
	base, digest, pinned := strings.Cut(running, "@sha256:")
	if !pinned || base != image || !validImageDigest(digest) {
		return Bundle{}, fmt.Errorf("operator image %q does not match embedded release image %q", running, image)
	}
	match := func(current string) (string, bool) { return running, current == image }
	out := bundle
	out.Spec.Image = running
	out.Spec.Components = slices.Clone(bundle.Spec.Components)
	for i, component := range out.Spec.Components {
		if component.Image == image {
			out.Spec.Components[i].Image = running
		}
	}
	out.Spec.Images = replaceImage(bundle.Spec.Images, image, running)
	out.Images = replaceImage(bundle.Images, image, running)
	out.Groups = slices.Clone(bundle.Groups)
	for i, group := range out.Groups {
		out.Groups[i].Objects = rewriteObjectImages(group.Objects, match)
	}
	out.Addons = maps.Clone(bundle.Addons)
	for name, objects := range bundle.Addons {
		out.Addons[name] = rewriteObjectImages(objects, match)
	}
	return out, nil
}

func validImageDigest(digest string) bool {
	if len(digest) != 2*sha256.Size || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func replaceImage(images []string, image, pinned string) []string {
	out := slices.Clone(images)
	for i, current := range out {
		if current == image {
			out[i] = pinned
		}
	}
	return out
}

func PinOwnImage(dir, digest string) error {
	value, found := strings.CutPrefix(digest, "sha256:")
	if !found || !validImageDigest(value) {
		return fmt.Errorf("digest %q is not a sha256 digest", digest)
	}
	spec, err := loadSpec(os.DirFS(dir))
	if err != nil {
		return err
	}
	image := spec.Image
	if strings.Contains(image, "@") {
		return fmt.Errorf("image %s is already pinned", image)
	}
	pattern := regexp.MustCompile(`(` + regexp.QuoteMeta(image) + `)(["'\s]|$)`)
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !isPinnable(path) {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pinned := pattern.ReplaceAll(content, []byte("${1}@"+digest+"${2}"))
		if string(pinned) == string(content) {
			return nil
		}
		return os.WriteFile(path, pinned, entry.Type().Perm()|0o644)
	})
}

func isPinnable(path string) bool {
	switch filepath.Ext(path) {
	case ".yaml", ".yml", ".txt":
		return true
	}
	return false
}
