package agent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	free, _ := deps.FreeBytes(filepath.Join(deps.Root, "var", "lib"))
	return &v1alpha1.HostChecks{
		TimeSynced:           timeSynced(ctx, deps.Exec),
		VarLibFreeBytes:      int64(free),
		CertificatesNotAfter: certificatesNotAfter(deps.Root),
		EtcdMembers:          etcdMembers(ctx, deps.Exec, deps.Root),
		ImagesBytes:          imagesBytes(deps.Root),
	}
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

func k0sVersion(ctx context.Context, e host.Exec) string {
	out, err := e.Run(ctx, k0s.DefaultBinary, "version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func timeSynced(ctx context.Context, e host.Exec) bool {
	out, err := e.Run(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
	return err == nil && strings.TrimSpace(out) == "yes"
}

func etcdMembers(ctx context.Context, e host.Exec, root string) int32 {
	if _, err := os.Stat(filepath.Join(root, "var", "lib", "k0s", "pki", "etcd")); err != nil {
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
