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

var certificateDirs = []string{"var/lib/k0s/pki", "var/lib/k0s/pki/etcd", "var/lib/kubelet/pki"}

func hostChecks(ctx context.Context, deps Deps) *v1alpha1.HostChecks {
	free, _ := deps.FreeBytes(filepath.Join(deps.Root, "var", "lib"))
	return &v1alpha1.HostChecks{
		TimeSynced:           timeSynced(ctx, deps.Exec),
		VarLibFreeBytes:      int64(free),
		CertificatesNotAfter: certificatesNotAfter(deps.Root),
		EtcdMembers:          etcdMembers(ctx, deps.Exec, deps.Root),
	}
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
	var earliest *metav1.Time
	for _, dir := range certificateDirs {
		for _, notAfter := range dirNotAfters(filepath.Join(root, dir)) {
			if earliest == nil || notAfter.Before(earliest.Time) {
				found := metav1.NewTime(notAfter)
				earliest = &found
			}
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
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		times = append(times, pemNotAfters(raw)...)
	}
	return times
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
