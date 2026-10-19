package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

func ApplyEncryption(ctx context.Context, c client.Client, deps Deps) error {
	var own v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: deps.Node}, &own); err != nil {
		return client.IgnoreNotFound(err)
	}
	grant := own.Annotations[v1alpha1.AnnotationEncryptionApply]
	if grant == "" || !v1alpha1.HostHasRole(own, v1alpha1.RoleControlPlane) || readEncryptionFailure(deps.Root) == grant {
		return nil
	}
	upgrading, err := nodeUpgrading(ctx, c, deps.Node)
	if err != nil || upgrading {
		return err
	}
	current, err := os.ReadFile(filepath.Join(deps.Root, encryption.File))
	if err != nil {
		return nil
	}
	if encryption.Hash(current) == grant && !encryptionRestartWanted(deps.Root) {
		return recordHandledGrant(deps.Root, grant)
	}
	target, err := readEncryptionSecret(ctx, deps)
	if err != nil {
		return err
	}
	if encryption.Hash(target) != grant {
		return recordHandledGrant(deps.Root, grant)
	}
	return errors.Join(applyEncryptionConfig(ctx, deps, current, target, grant), recordHandledGrant(deps.Root, grant))
}

func recordHandledGrant(root, grant string) error {
	return os.WriteFile(filepath.Join(root, encryptionDir, encryptionHandled), []byte(grant), 0o600)
}

func encryptionRestartWanted(root string) bool {
	start, ok := apiserverStartTime(root)
	return ok && encryptionRestartPending(root, start)
}

func readEncryptionSecret(ctx context.Context, deps Deps) ([]byte, error) {
	out, err := adminKubectl(ctx, deps, "--namespace", encryption.SecretNamespace, "get", "secret", encryption.SecretName, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("read secret %s/%s: %w", encryption.SecretNamespace, encryption.SecretName, err)
	}
	var secret corev1.Secret
	if err := json.Unmarshal([]byte(out), &secret); err != nil {
		return nil, fmt.Errorf("read secret %s/%s: %w", encryption.SecretNamespace, encryption.SecretName, err)
	}
	content := secret.Data[encryption.SecretKey]
	if len(content) == 0 {
		return nil, fmt.Errorf("secret %s/%s holds no %s", encryption.SecretNamespace, encryption.SecretName, encryption.SecretKey)
	}
	return content, nil
}

func applyEncryptionConfig(ctx context.Context, deps Deps, current, target []byte, grant string) error {
	retired := ""
	if encryption.DropsKey(current, target) {
		path, err := retireEncryptionFile(deps, current)
		if err != nil {
			return err
		}
		retired = path
	}
	if err := writeEncryptionFile(deps, target); err != nil {
		return errors.Join(err, removeIfExists(retired))
	}
	restartErr := restartForEncryption(ctx, deps)
	if restartErr == nil {
		return removeIfExists(filepath.Join(deps.Root, encryptionDir, encryptionFailure))
	}
	return errors.Join(restartErr, rollbackEncryption(ctx, deps, current, retired, grant))
}

func rollbackEncryption(ctx context.Context, deps Deps, previous []byte, retired, grant string) error {
	if ctx.Err() != nil {
		return nil
	}
	return errors.Join(
		writeEncryptionFile(deps, previous),
		removeIfExists(retired),
		recordEncryptionFailure(deps.Root, grant),
		restartForEncryption(ctx, deps),
	)
}

func writeEncryptionFile(deps Deps, content []byte) error {
	_, err := apiserver.WriteFiles(filepath.Join(deps.Root, encryptionDir), map[string][]byte{encryption.FileName: content}, deps.AuthnOwner)
	return err
}

func retireEncryptionFile(deps Deps, content []byte) (string, error) {
	dir := filepath.Join(deps.Root, encryptionDir, encryptionRetired)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, deps.Now().UTC().Format(stampLayout)+encryptionFileStamp)
	err := writeAtomic(path, func(dst io.Writer) error {
		_, err := dst.Write(content)
		return err
	})
	return path, err
}

func recordEncryptionFailure(root, grant string) error {
	return os.WriteFile(filepath.Join(root, encryptionDir, encryptionFailure), []byte(grant), 0o600)
}

func restartForEncryption(ctx context.Context, deps Deps) error {
	if _, err := deps.Exec.Run(ctx, "systemctl", "restart", "--no-block", k0sControllerUnit); err != nil {
		return err
	}
	observed, err := waitClear(ctx, deps.K0sTimeout, deps.K0sPoll, func(probeCtx context.Context) string {
		return encryptionRestartProblem(probeCtx, deps)
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("kube-apiserver did not restart within %s: %s", deps.K0sTimeout, observed)
	}
	return err
}

func encryptionRestartProblem(ctx context.Context, deps Deps) string {
	start, ok := apiserverStartTime(deps.Root)
	if !ok {
		return "kube-apiserver does not run"
	}
	if encryptionRestartPending(deps.Root, start) {
		return "kube-apiserver still runs from before " + encryption.File
	}
	return bounded(ctx, deps.ProbeTimeout, func(probeCtx context.Context) string {
		return localReadyzProblem(probeCtx, deps)
	})
}

func localReadyzProblem(ctx context.Context, deps Deps) string {
	out, err := deps.Exec.Run(ctx, k0s.DefaultBinary, "kubectl", "--kubeconfig", filepath.Join(deps.Root, adminKubeconfig), "--server", localAPIServer, "--request-timeout=10s", "get", "--raw", "/readyz")
	if err != nil {
		return fmt.Sprintf("/readyz: %v", err)
	}
	if answer := strings.TrimSpace(out); answer != "ok" {
		return fmt.Sprintf("/readyz: %s", answer)
	}
	return ""
}
