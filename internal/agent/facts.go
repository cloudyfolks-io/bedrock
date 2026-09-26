package agent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/k0s"
)

var certificateDirs = []string{"var/lib/k0s/pki", "var/lib/k0s/pki/etcd"}

var kubeletCertificates = []string{"var/lib/kubelet/pki/kubelet-client-current.pem", "var/lib/kubelet/pki/kubelet-server-current.pem"}

func hostChecks(ctx context.Context, deps Deps) *v1alpha1.HostChecks {
	space, _ := deps.DiskSpace(filepath.Join(deps.Root, "var", "lib"))
	return &v1alpha1.HostChecks{
		TimeSynced:           bounded(ctx, deps.ProbeTimeout, func(c context.Context) bool { return timeSynced(c, deps.Exec) }),
		VarLibFreeBytes:      int64(space.FreeBytes),
		VarLibSizeBytes:      int64(space.SizeBytes),
		CertificatesNotAfter: certificatesNotAfter(deps.Root),
		EtcdMembers:          bounded(ctx, deps.ProbeTimeout, func(c context.Context) int32 { return etcdMembers(c, deps.Exec, deps.Root) }),
		EtcdHealthy:          bounded(ctx, deps.ProbeTimeout, func(c context.Context) bool { return etcdHealthy(c, deps.Exec, deps.Root) }),
		ImagesBytes:          imagesBytes(deps.Root),
	}
}

func probedK0sVersion(ctx context.Context, deps Deps) string {
	return bounded(ctx, deps.ProbeTimeout, func(probeCtx context.Context) string { return runningK0sVersion(probeCtx, deps.Exec) })
}

func bounded[T any](ctx context.Context, limit time.Duration, read func(context.Context) T) T {
	probeCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return read(probeCtx)
}

func imagesBytes(root string) int64 {
	entries, err := os.ReadDir(filepath.Join(root, k0sImagesDir))
	if err != nil {
		return 0
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		total += info.Size()
	}
	return total
}

func hostname(deps Deps) string {
	name, err := deps.Hostname()
	if err != nil {
		return ""
	}
	return strings.ToLower(name)
}

func runningK0sVersion(ctx context.Context, e host.Exec) string {
	out, err := e.Run(ctx, k0s.DefaultBinary, "status", "-o", "json")
	if err != nil {
		return ""
	}
	var status struct {
		Version string
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return ""
	}
	return status.Version
}

func timeSynced(ctx context.Context, e host.Exec) bool {
	out, err := e.Run(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
	return err == nil && strings.TrimSpace(out) == "yes"
}

func hasEtcd(root string) bool {
	_, err := os.Stat(filepath.Join(root, "var", "lib", "k0s", "pki", "etcd"))
	return err == nil
}

func etcdHealthy(ctx context.Context, e host.Exec, root string) bool {
	return hasEtcd(root) && apiProblem(ctx, e, root, "/readyz/etcd") == ""
}

func apiProblem(ctx context.Context, e host.Exec, root, path string) string {
	out, err := e.Run(ctx, k0s.DefaultBinary, "kubectl", "--kubeconfig", filepath.Join(root, adminKubeconfig), "--request-timeout=10s", "get", "--raw", path)
	if err != nil {
		return fmt.Sprintf("%s: %v", path, err)
	}
	if answer := strings.TrimSpace(out); answer != "ok" {
		return fmt.Sprintf("%s: %s", path, answer)
	}
	return ""
}

func etcdMembers(ctx context.Context, e host.Exec, root string) int32 {
	if !hasEtcd(root) {
		return 0
	}
	out, err := e.Run(ctx, k0s.DefaultBinary, "etcd", "member-list")
	if err != nil {
		return 0
	}
	var list struct {
		Members map[string]string `json:"members"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return 0
	}
	return int32(len(list.Members))
}

func certificatesNotAfter(root string) *metav1.Time {
	var times []time.Time
	for _, dir := range certificateDirs {
		times = append(times, dirNotAfters(filepath.Join(root, dir))...)
	}
	for _, file := range kubeletCertificates {
		times = append(times, fileNotAfters(filepath.Join(root, file))...)
	}
	var earliest *metav1.Time
	for _, notAfter := range times {
		if earliest == nil || notAfter.Before(earliest.Time) {
			found := metav1.NewTime(notAfter)
			earliest = &found
		}
	}
	return earliest
}

func dirNotAfters(dir string) []time.Time {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var times []time.Time
	for _, entry := range entries {
		if entry.IsDir() || !(strings.HasSuffix(entry.Name(), ".crt") || strings.HasSuffix(entry.Name(), ".pem")) {
			continue
		}
		times = append(times, fileNotAfters(filepath.Join(dir, entry.Name()))...)
	}
	return times
}

func fileNotAfters(path string) []time.Time {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return pemNotAfters(raw)
}

func pemNotAfters(raw []byte) []time.Time {
	var times []time.Time
	for block, rest := pem.Decode(raw); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			times = append(times, cert.NotAfter)
		}
	}
	return times
}
