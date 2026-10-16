package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

const pkiDirMode = 0o751

type encryptionFacts struct {
	path      string
	content   []byte
	found     bool
	installed bool
	named     bool
}

func requireSockets(stat func(string) (fs.FileInfo, error), root string, paths []string) error {
	for _, path := range paths {
		info, err := stat(filepath.Join(root, path))
		if err != nil || info.Mode()&fs.ModeSocket == 0 {
			return fmt.Errorf("kms plugin socket %s is missing: start the KMS plugin on this node first", path)
		}
	}
	return nil
}

func readEncryptionFacts(path, k0sConfigPath, dataDir string, running bool) (encryptionFacts, error) {
	facts := encryptionFacts{path: path}
	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		facts.content, facts.found = content, true
	case !errors.Is(err, fs.ErrNotExist):
		return facts, fmt.Errorf("%s: %w", path, err)
	}
	hasEtcd, err := dirExists(filepath.Join(dataDir, "etcd"))
	if err != nil {
		return facts, err
	}
	facts.installed = running || hasEtcd
	if !facts.installed {
		return facts, nil
	}
	facts.named, err = k0sNamesEncryption(k0sConfigPath)
	return facts, err
}

func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil && info.IsDir(), err
}

func k0sNamesEncryption(k0sConfigPath string) (bool, error) {
	current, err := os.ReadFile(k0sConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	named, err := k0s.APIExtraArg(current, encryption.ArgName)
	return named != "", err
}

func chooseEncryption(facts encryptionFacts, generate func() ([]byte, error)) ([]byte, error) {
	switch {
	case facts.found && len(facts.content) == 0:
		return nil, fmt.Errorf("%s is empty", facts.path)
	case !facts.installed && facts.found:
		return facts.content, nil
	case !facts.installed:
		return generate()
	case facts.named && facts.found:
		return facts.content, nil
	case facts.named:
		return nil, fmt.Errorf("%s is missing but k0s.yaml names it: restore it from Secret %s/%s or a k0s backup", facts.path, encryption.SecretNamespace, encryption.SecretName)
	case facts.found:
		return nil, fmt.Errorf("%s exists but k0s.yaml does not name it: init does not enable encryption on an installed node, remove the file", facts.path)
	}
	return nil, nil
}

func requireEncryptionSockets(stat func(string) (fs.FileInfo, error), root, path string, content []byte) error {
	paths, err := encryption.SocketPaths(content)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return requireSockets(stat, root, paths)
}

func installEncryptionFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), pkiDirMode); err != nil {
		return err
	}
	_, err := apiserver.WriteFile(path, content, apiserver.APIServerOwner())
	return err
}

func ensureEncryptionSecret(ctx context.Context, c client.Client, path string, content []byte) error {
	err := c.Create(ctx, encryption.Secret(content), client.FieldOwner(initFieldOwner))
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := encryption.ReadSecret(ctx, c)
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, content) {
		return fmt.Errorf("secret %s/%s holds another encryption config than %s", encryption.SecretNamespace, encryption.SecretName, path)
	}
	return nil
}
