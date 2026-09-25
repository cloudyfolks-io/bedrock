package host

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func ReplaceFile(path, content string, perm os.FileMode) (bool, error) {
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	existed := err == nil
	if existed && bytes.Equal(current, []byte(content)) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		return false, err
	}
	return existed, nil
}
