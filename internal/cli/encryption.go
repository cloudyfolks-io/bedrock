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

const joinFetchTimeout = 30 * time.Second

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

func readEncryptionFacts(root, k0sConfigPath, dataDir string, running bool) (encryptionFacts, error) {
	facts := encryptionFacts{path: encryption.File}
	content, err := os.ReadFile(filepath.Join(root, encryption.File))
	switch {
	case err == nil:
		facts.content, facts.found = content, true
	case !errors.Is(err, fs.ErrNotExist):
		return facts, fmt.Errorf("%s: %w", encryption.File, err)
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
	switch {
	case err != nil:
		return false, err
	case named == "":
		return false, nil
	case named != encryption.File:
		return false, fmt.Errorf("k0s.yaml names %s as the encryption config, not %s", named, encryption.File)
	}
	return true, nil
}

func requireEncryptionPath(k0sConfig []byte) error {
	named, err := k0s.APIExtraArg(k0sConfig, encryption.ArgName)
	if err != nil || named == "" || named == encryption.File {
		return err
	}
	return fmt.Errorf("the cluster keeps its encryption config at %s, this bedrock version expects %s", named, encryption.File)
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
		return nil, fmt.Errorf("%s is missing but k0s.yaml names it: copy it from another control-plane node or from your separate key copy, or read Secret %s/%s through another control-plane node", facts.path, encryption.SecretNamespace, encryption.SecretName)
	case facts.found:
		return nil, fmt.Errorf("%s exists but k0s.yaml does not name it: add %s=%s back to the api extraArgs of k0s.yaml if this node ever ran encrypted, remove the file only if it never did", facts.path, encryption.ArgName, facts.path)
	}
	return nil, nil
}

func requireEncryptionSockets(stat func(string) (fs.FileInfo, error), root string, content []byte) error {
	paths, err := encryption.SocketPaths(content)
	if err != nil {
		return fmt.Errorf("%s: %w", encryption.File, err)
	}
	return requireSockets(stat, root, paths)
}

func installEncryptionFile(root string, content []byte) error {
	_, err := apiserver.WriteFiles(filepath.Join(root, encryption.Dir), map[string][]byte{encryption.FileName: content}, apiserver.APIServerOwner())
	return err
}

func ensureEncryptionSecret(ctx context.Context, c client.Client, content []byte) error {
	err := c.Create(ctx, encryption.Secret(content), client.FieldOwner(initFieldOwner))
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := encryption.ReadSecret(ctx, c)
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, content) {
		return fmt.Errorf("secret %s/%s holds another encryption config than %s", encryption.SecretNamespace, encryption.SecretName, encryption.File)
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

func fetchJoinEncryption(ctx context.Context, stdout io.Writer, deps InitDeps, token k0s.Token) ([]byte, error) {
	named, err := k0s.APIExtraArg(token.K0sConfig, encryption.ArgName)
	if err != nil || named == "" {
		return nil, err
	}
	step(stdout, "secrets encryption")
	content, err := FetchEncryptionFile(ctx, token, deps.NewConfigClient)
	if err != nil {
		return nil, err
	}
	if err := requireEncryptionSockets(deps.Stat, deps.Root, content); err != nil {
		return nil, err
	}
	return content, nil
}
