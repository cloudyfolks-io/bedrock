package seal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
)

const (
	RetiredDir    = HostKeyDir + "/retired"
	StampLayout   = "20060102T150405Z"
	retiredSuffix = ".key"
)

type RetiredKey struct {
	Path string
	At   time.Time
}

func RotateHostIdentity(root string, now time.Time) (*age.X25519Identity, error) {
	path := filepath.Join(root, HostKeyFile)
	if _, err := readIdentity(path); err != nil {
		return nil, fmt.Errorf("no host key to rotate: %w", err)
	}
	retiredDir := filepath.Join(root, RetiredDir)
	if err := ensureDir(retiredDir); err != nil {
		return nil, err
	}
	if err := os.Link(path, filepath.Join(retiredDir, now.UTC().Format(StampLayout)+retiredSuffix)); err != nil {
		return nil, fmt.Errorf("retire the host key: %w", err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	return identity, replaceFile(path, KeyFile(identity))
}

func replaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".host.key-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := errors.Join(fillSynced(temp, data), temp.Close()); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

func RetiredHostKeys(root string) ([]RetiredKey, error) {
	entries, err := os.ReadDir(filepath.Join(root, RetiredDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []RetiredKey
	for _, entry := range entries {
		stamp, isKey := strings.CutSuffix(entry.Name(), retiredSuffix)
		at, parseErr := time.Parse(StampLayout, stamp)
		if isKey && parseErr == nil {
			keys = append(keys, RetiredKey{Path: filepath.Join(root, RetiredDir, entry.Name()), At: at})
		}
	}
	slices.SortFunc(keys, func(a, b RetiredKey) int { return b.At.Compare(a.At) })
	return keys, nil
}

func HostIdentities(root string) ([]age.Identity, error) {
	current, err := readIdentity(filepath.Join(root, HostKeyFile))
	if err != nil {
		return nil, err
	}
	identities := []age.Identity{current}
	retired, err := RetiredHostKeys(root)
	if err != nil {
		return nil, err
	}
	for _, key := range retired {
		if identity, err := readIdentity(key.Path); err == nil {
			identities = append(identities, identity)
		}
	}
	return identities, nil
}
