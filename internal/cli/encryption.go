package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

const (
	pkiDirMode       = 0o751
	joinFetchTimeout = 30 * time.Second
)

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
	facts.named, err = k0sNamesEncryption(k0sConfigPath, path)
	return facts, err
}

func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil && info.IsDir(), err
}

func k0sNamesEncryption(k0sConfigPath, path string) (bool, error) {
	current, err := os.ReadFile(k0sConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	named, err := k0s.APIExtraArg(current, encryption.ArgName)
	switch {
	case err != nil:
		return false, err
	case named == "":
		return false, nil
	case named != path:
		return false, fmt.Errorf("k0s.yaml names %s as the encryption config, not %s", named, path)
	}
	return true, nil
}

func requireEncryptionDataDir(k0sConfig []byte, dataDir string) error {
	named, err := k0s.APIExtraArg(k0sConfig, encryption.ArgName)
	if err != nil || named == "" || named == encryption.Path(dataDir) {
		return err
	}
	return fmt.Errorf("the cluster keeps its encryption config at %s, join with --data-dir %s", named, filepath.Dir(filepath.Dir(named)))
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
		return nil, fmt.Errorf("%s is missing but k0s.yaml names it: restore it from a k0s backup, or read Secret %s/%s through another control-plane node", facts.path, encryption.SecretNamespace, encryption.SecretName)
	case facts.found:
		return nil, fmt.Errorf("%s exists but k0s.yaml does not name it: add %s=%s back to the api extraArgs of k0s.yaml if this node ever ran encrypted, remove the file only if it never did", facts.path, encryption.ArgName, facts.path)
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

func FetchEncryptionFile(ctx context.Context, token k0s.Token, newClient func(*rest.Config) (client.Client, error)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, joinFetchTimeout)
	defer cancel()
	ca, err := k0s.FetchCA(ctx, token.K0sToken)
	if err != nil {
		return nil, fmt.Errorf("fetch the cluster CA: %w", err)
	}
	content, err := readEncryptionSecret(ctx, ca, token.VIP, newClient)
	if err != nil {
		return nil, fmt.Errorf("read the encryption config: %w", err)
	}
	return content, nil
}

func readEncryptionSecret(ctx context.Context, ca k0s.CA, vip string, newClient func(*rest.Config) (client.Client, error)) ([]byte, error) {
	config, err := encryption.JoinClientConfig(ca, vip, time.Now())
	if err != nil {
		return nil, err
	}
	c, err := newClient(config)
	if err != nil {
		return nil, err
	}
	return encryption.ReadSecret(ctx, c)
}

func fetchJoinEncryption(ctx context.Context, stdout io.Writer, deps InitDeps, token k0s.Token, dataDir string) ([]byte, error) {
	named, err := k0s.APIExtraArg(token.K0sConfig, encryption.ArgName)
	if err != nil || named == "" {
		return nil, err
	}
	step(stdout, "secrets encryption")
	content, err := FetchEncryptionFile(ctx, token, deps.NewConfigClient)
	if err != nil {
		return nil, err
	}
	if err := requireEncryptionSockets(deps.Stat, deps.Root, encryption.Path(dataDir), content); err != nil {
		return nil, err
	}
	return content, nil
}
