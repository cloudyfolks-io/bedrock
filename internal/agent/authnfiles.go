package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	apiserverProcess    = "kube-apiserver"
	apiserverStartSlack = 2 * time.Second
	clockTick           = 10 * time.Millisecond
	startTimeField      = 19
)

func syncAuthnFiles(ctx context.Context, c client.Client, deps Deps, current v1alpha1.Host) *v1alpha1.AuthnFilesStatus {
	if !v1alpha1.HostHasRole(current, v1alpha1.RoleControlPlane) {
		return nil
	}
	previous := previousAuthn(current.Status.Authn)
	service := k0sServiceOf(current)
	files, err := authnSources(ctx, c)
	if err != nil {
		return keptAuthn(previous, deps.Root, service, err.Error())
	}
	written, err := apiserver.WriteFiles(filepath.Join(deps.Root, authnDir), files, deps.AuthnOwner)
	if err != nil {
		return keptAuthn(previous, deps.Root, service, err.Error())
	}
	next := v1alpha1.AuthnFilesStatus{
		Hash:                  apiserver.Hash(files[apiserver.AuthenticationFile], files[apiserver.WebhookFile]),
		WrittenAt:             previous.WrittenAt,
		WebhookRestartPending: authnRestartWanted(deps.Root, service),
	}
	if written {
		now := metav1.NewTime(deps.Now())
		next.WrittenAt = &now
	}
	return &next
}

func previousAuthn(status *v1alpha1.AuthnFilesStatus) v1alpha1.AuthnFilesStatus {
	if status == nil {
		return v1alpha1.AuthnFilesStatus{}
	}
	return *status.DeepCopy()
}

func keptAuthn(previous v1alpha1.AuthnFilesStatus, root string, service k0sService, message string) *v1alpha1.AuthnFilesStatus {
	previous.Message = message
	previous.WebhookRestartPending = authnRestartWanted(root, service)
	return &previous
}

func authnSources(ctx context.Context, c client.Client) (map[string][]byte, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: apiserver.ConfigMapName}, &cm); err != nil {
		return nil, sourceError(apiserver.ConfigMapName, err)
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: apiserver.SecretName}, &secret); err != nil {
		return nil, sourceError(apiserver.SecretName, err)
	}
	authentication := cm.Data[apiserver.AuthenticationFile]
	if authentication == "" {
		return nil, waitingFor(apiserver.ConfigMapName)
	}
	webhook := secret.Data[apiserver.WebhookFile]
	if len(webhook) == 0 {
		return nil, waitingFor(apiserver.SecretName)
	}
	return map[string][]byte{apiserver.AuthenticationFile: []byte(authentication), apiserver.WebhookFile: webhook}, nil
}

func sourceError(name string, err error) error {
	if errors.IsNotFound(err) {
		return waitingFor(name)
	}
	return err
}

func waitingFor(name string) error {
	return fmt.Errorf("waiting for %s", name)
}

func webhookRestartPending(root string, apiserverStart time.Time) bool {
	info, err := os.Stat(filepath.Join(root, authnDir, apiserver.WebhookFile))
	return err == nil && info.ModTime().After(apiserverStart.Add(apiserverStartSlack))
}

func apiserverStartTime(root string) (time.Time, bool) {
	boot, ok := bootTime(filepath.Join(root, "proc", "stat"))
	if !ok {
		return time.Time{}, false
	}
	dir, ok := apiserverProcessDir(root)
	if !ok {
		return time.Time{}, false
	}
	ticks, ok := startTicks(filepath.Join(dir, "stat"))
	if !ok {
		return time.Time{}, false
	}
	return boot.Add(time.Duration(ticks) * clockTick), true
}

func apiserverProcessDir(root string) (string, bool) {
	binary, err := os.Stat(filepath.Join(root, k0sDataDir, "bin", apiserverProcess))
	if err != nil {
		return "", false
	}
	entries, err := os.ReadDir(filepath.Join(root, "proc"))
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		dir := filepath.Join(root, "proc", entry.Name())
		if isPID(entry.Name()) && processName(dir) == apiserverProcess && runsBinary(dir, binary) {
			return dir, true
		}
	}
	return "", false
}

func runsBinary(dir string, binary os.FileInfo) bool {
	exe, err := os.Stat(filepath.Join(dir, "exe"))
	return err == nil && os.SameFile(exe, binary)
}

func isPID(name string) bool {
	_, err := strconv.Atoi(name)
	return err == nil
}

func processName(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func bootTime(path string) (time.Time, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		value, found := strings.CutPrefix(line, "btime ")
		if !found {
			continue
		}
		seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(seconds, 0), true
	}
	return time.Time{}, false
}

func startTicks(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	stat := string(raw)
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) <= startTimeField {
		return 0, false
	}
	ticks, err := strconv.ParseInt(fields[startTimeField], 10, 64)
	return ticks, err == nil
}
