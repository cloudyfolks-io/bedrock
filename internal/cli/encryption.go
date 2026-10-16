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

func requireSockets(stat func(string) (fs.FileInfo, error), root string, paths []string) error {
	for _, path := range paths {
		info, err := stat(filepath.Join(root, path))
		if err != nil || info.Mode()&fs.ModeSocket == 0 {
			return fmt.Errorf("kms plugin socket %s is missing: start the KMS plugin on this node first", path)
		}
	}
	return nil
}

func readEncryptionFile(path string) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return content, err == nil, err
}

func requireNoEncryptionArg(k0sConfigPath, path string) error {
	current, err := os.ReadFile(k0sConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	named, err := k0s.APIExtraArg(current, encryption.ArgName)
	if err != nil {
		return err
	}
	if named != "" {
		return fmt.Errorf("%s is missing but k0s.yaml names it: restore it from Secret %s/%s or a k0s backup", path, encryption.SecretNamespace, encryption.SecretName)
	}
	return nil
}

func requireEncryptionSockets(stat func(string) (fs.FileInfo, error), root string, content []byte) error {
	paths, err := encryption.SocketPaths(content)
	if err != nil {
		return err
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
