package apiserver

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
)

const (
	APIServerUser = "kube-apiserver"
	fileMode      = 0o600
	dirMode       = 0o700
	parentMode    = 0o755
)

type Owner func(path string) error

func KeepOwner(string) error {
	return nil
}

func APIServerOwner() Owner {
	account, err := user.Lookup(APIServerUser)
	if err != nil {
		return KeepOwner
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil {
		return KeepOwner
	}
	return func(path string) error {
		return os.Chown(path, uid, gid)
	}
}

func WriteFiles(dir string, files map[string][]byte, owner Owner) (bool, error) {
	if err := traversableParent(filepath.Dir(dir)); err != nil {
		return false, err
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return false, err
	}
	if err := settle(dir, dirMode, owner); err != nil {
		return false, err
	}
	changed := false
	for _, name := range slices.Sorted(maps.Keys(files)) {
		written, err := writeFile(filepath.Join(dir, name), files[name], owner)
		if err != nil {
			return false, err
		}
		changed = changed || written
	}
	return changed, nil
}

func traversableParent(parent string) error {
	if err := os.MkdirAll(parent, parentMode); err != nil {
		return err
	}
	return os.Chmod(parent, parentMode)
}

func writeFile(path string, content []byte, owner Owner) (bool, error) {
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err == nil && bytes.Equal(current, content) {
		return false, settle(path, fileMode, owner)
	}
	next, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return false, err
	}
	defer os.Remove(next.Name())
	if err := writeTemp(next, content, owner); err != nil {
		return false, err
	}
	if err := os.Rename(next.Name(), path); err != nil {
		return false, err
	}
	return true, syncDir(filepath.Dir(path))
}

func writeTemp(next *os.File, content []byte, owner Owner) error {
	defer next.Close()
	if _, err := next.Write(content); err != nil {
		return err
	}
	if err := owner(next.Name()); err != nil {
		return err
	}
	return next.Sync()
}

func syncDir(dir string) error {
	opened, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer opened.Close()
	return opened.Sync()
}

func settle(path string, mode os.FileMode, owner Owner) error {
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return owner(path)
}
