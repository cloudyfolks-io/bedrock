package release

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func PinOwnImage(dir, digest string) error {
	if !strings.HasPrefix(digest, "sha256:") {
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
