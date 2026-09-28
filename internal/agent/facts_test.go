package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func writeCertificate(t *testing.T, path string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: filepath.Base(path)}, NotBefore: notAfter.Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedProbeReportsItsLimit(t *testing.T) {
	problem := bounded(context.Background(), 50*time.Millisecond, func(probeCtx context.Context) string {
		_, err := host.RealExec{}.Run(probeCtx, "sleep", "10")
		return fmt.Sprint(err)
	})
	if problem != "sleep 10: timed out after 50ms: " {
		t.Fatalf("problem %q", problem)
	}
}

func TestCertificatesNotAfterFindsTheEarliest(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	writeCertificate(t, filepath.Join(root, "var/lib/k0s/pki/server.crt"), base.Add(48*time.Hour))
	writeCertificate(t, filepath.Join(root, "var/lib/k0s/pki/etcd/peer.crt"), base.Add(72*time.Hour))
	writeCertificate(t, filepath.Join(root, "var/lib/kubelet/pki/kubelet-client-current.pem"), base)
	if err := os.WriteFile(filepath.Join(root, "var/lib/k0s/pki/server.key"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := certificatesNotAfter(root)
	if got == nil || !got.Time.Equal(base) {
		t.Fatalf("earliest notAfter %v, want %v", got, base)
	}
}

func TestCertificatesNotAfterReadsOnlyTheKubeletCertificatesInUse(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	kubelet := filepath.Join(root, "var/lib/kubelet/pki")
	writeCertificate(t, filepath.Join(root, "var/lib/k0s/pki/server.crt"), base.Add(48*time.Hour))
	writeCertificate(t, filepath.Join(kubelet, "kubelet-client-2027-01-01-00-00-00.pem"), base.Add(24*time.Hour))
	if err := os.Symlink("kubelet-client-2027-01-01-00-00-00.pem", filepath.Join(kubelet, "kubelet-client-current.pem")); err != nil {
		t.Fatal(err)
	}
	writeCertificate(t, filepath.Join(kubelet, "kubelet-server-current.pem"), base.Add(12*time.Hour))
	writeCertificate(t, filepath.Join(kubelet, "kubelet-client-2026-01-01-00-00-00.pem"), base.Add(-365*24*time.Hour))
	writeCertificate(t, filepath.Join(kubelet, "kubelet.crt"), base.Add(-30*24*time.Hour))
	got := certificatesNotAfter(root)
	if want := base.Add(12 * time.Hour); got == nil || !got.Time.Equal(want) {
		t.Fatalf("earliest notAfter %v, want %v: a rotated client certificate and kubelet.crt must not count", got, want)
	}
}

func TestCertificatesNotAfterWithoutCertificates(t *testing.T) {
	if got := certificatesNotAfter(t.TempDir()); got != nil {
		t.Fatalf("no certificates must give nil, got %v", got)
	}
}

func TestEtcdMembers(t *testing.T) {
	root := t.TempDir()
	exec := &host.FakeExec{Responses: map[string]string{"/usr/local/bin/k0s etcd member-list": `{"members":{"a":"https://10.0.0.1:2380","b":"https://10.0.0.2:2380"}}`}}
	if got := etcdMembers(context.Background(), exec, root); got != 0 || len(exec.Calls) != 0 {
		t.Fatalf("a host without etcd must report 0 without running k0s, got %d calls %v", got, exec.Calls)
	}
	if err := os.MkdirAll(filepath.Join(root, "var/lib/k0s/pki/etcd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := etcdMembers(context.Background(), exec, root); got != 2 {
		t.Fatalf("members %d, want 2", got)
	}
}

func TestEtcdHealthy(t *testing.T) {
	root := t.TempDir()
	probe := "/usr/local/bin/k0s kubectl --kubeconfig " + filepath.Join(root, "var/lib/k0s/pki/admin.conf") + " --request-timeout=10s get --raw /readyz/etcd"
	healthy := &host.FakeExec{Responses: map[string]string{probe: "ok"}}
	if etcdHealthy(context.Background(), healthy, root) || len(healthy.Calls) != 0 {
		t.Fatalf("a host without etcd is not healthy and runs nothing, calls %v", healthy.Calls)
	}
	if err := os.MkdirAll(filepath.Join(root, "var/lib/k0s/pki/etcd"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		exec *host.FakeExec
		want bool
	}{
		"ok":      {healthy, true},
		"failing": {&host.FakeExec{Responses: map[string]string{probe: "[-]etcd failed: reason withheld"}}, false},
		"down":    {&host.FakeExec{Errors: map[string]error{probe: errors.New("exit status 1")}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := etcdHealthy(context.Background(), tc.exec, root); got != tc.want {
				t.Fatalf("healthy %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTimeSynced(t *testing.T) {
	key := "timedatectl show -p NTPSynchronized --value"
	cases := []struct {
		exec *host.FakeExec
		want bool
	}{
		{&host.FakeExec{Responses: map[string]string{key: "yes\n"}}, true},
		{&host.FakeExec{Responses: map[string]string{key: "no\n"}}, false},
		{&host.FakeExec{Errors: map[string]error{key: errors.New("no timedatectl")}}, false},
	}
	for _, tc := range cases {
		if got := timeSynced(context.Background(), tc.exec); got != tc.want {
			t.Fatalf("timeSynced %v, want %v", got, tc.want)
		}
	}
}

func TestImagesBytesSumsTheImageFiles(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, k0sImagesDir, "a.tar"), "01234")
	writeFixtureFile(t, filepath.Join(root, k0sImagesDir, "b.tar"), "0123456789")
	if got := imagesBytes(root); got != 15 {
		t.Fatalf("imagesBytes %d, want 15", got)
	}
}

func TestImagesBytesWithoutTheDirectory(t *testing.T) {
	if got := imagesBytes(t.TempDir()); got != 0 {
		t.Fatalf("imagesBytes %d, want 0", got)
	}
}

func TestRunningK0sVersion(t *testing.T) {
	status := "/usr/local/bin/k0s status -o json"
	running := &host.FakeExec{Responses: map[string]string{status: `{"Version": "v1.36.3+k0s.0", "Pid": 1234, "Role": "controller+worker"}`}}
	if got := runningK0sVersion(context.Background(), running); got != "v1.36.3+k0s.0" {
		t.Fatalf("running k0s version %q", got)
	}
	if got := runningK0sVersion(context.Background(), &host.FakeExec{}); got != "" {
		t.Fatalf("k0s that does not run must give an empty version, got %q", got)
	}
	garbled := &host.FakeExec{Responses: map[string]string{status: "Version: v1.36.3+k0s.0"}}
	if got := runningK0sVersion(context.Background(), garbled); got != "" {
		t.Fatalf("a status that is not JSON must give an empty version, got %q", got)
	}
}
