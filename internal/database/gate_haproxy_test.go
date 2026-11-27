package database

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	must(t, err)
	cert, err := x509.ParseCertificate(der)
	must(t, err)
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca testCA) issue(t *testing.T, serial int64, name string, usage x509.ExtKeyUsage, dns []string, ips []net.IP) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, DNSNames: dns, IPAddresses: ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	must(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	must(t, err)
	var out bytes.Buffer
	must(t, pem.Encode(&out, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	must(t, pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return out.Bytes()
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		must(t, os.WriteFile(filepath.Join(dir, name), content, 0o644))
	}
	return dir
}

func startHAProxy(t *testing.T, image, dir string, publish bool) (name string) {
	t.Helper()
	name = fmt.Sprintf("p26b-haproxy-%d", time.Now().UnixNano())
	args := []string{"run", "-d", "--name", name, "--user", "1000:1000", "-v", dir + ":/certs:ro"}
	if publish {
		args = append(args, "-p", "127.0.0.1::5432")
	}
	args = append(args, image, "haproxy", "-f", "/certs/haproxy.cfg")
	docker(t, args...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	return name
}

func echoServer(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	must(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err == nil {
					_, _ = conn.Write([]byte(line))
				}
			}()
		}
	}()
	return listener
}

func through(t *testing.T, address string) (string, error) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = conn.Write([]byte("hello\n"))
			line, readErr := bufio.NewReader(conn).ReadString('\n')
			conn.Close()
			if readErr == nil || time.Now().After(deadline) {
				return line, readErr
			}
		}
		if time.Now().After(deadline) {
			return "", err
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func awaitHAProxyStatus(t *testing.T, name, path string, status int) {
	t.Helper()
	address := fmt.Sprintf("http://127.0.0.1:%d%s", HealthPort, path)
	want := fmt.Sprintf("HTTP/1.1 %d ", status)
	deadline := time.Now().Add(15 * time.Second)
	for {
		out, err := exec.Command("docker", "exec", name, "wget", "-S", "-O", "/dev/null", "-T", "2", address).CombinedOutput()
		if strings.Contains(string(out), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("HAProxy %s did not return %d for %s: %v\n%s", name, status, path, err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestGateAcceptsItsOwnProxyAndRefusesEveryOtherClientWithRealHAProxy(t *testing.T) {
	image := os.Getenv("BEDROCK_TEST_HAPROXY_IMAGE")
	if image == "" {
		t.Skip("set BEDROCK_TEST_HAPROXY_IMAGE to run the gate against a real HAProxy through docker")
	}
	ca, strangerCA := newTestCA(t, "database-ca"), newTestCA(t, "stranger-ca")
	serverName := ServerName(gateUID)
	server := ca.issue(t, 10, gateUID, 1, []string{serverName, "localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	backend := echoServer(t)
	backendPort := backend.Addr().(*net.TCPAddr).Port
	hostGateway := docker(t, "network", "inspect", "bridge", "-f", "{{(index .IPAM.Config 0).Gateway}}")

	gateConfig, err := GateConfig(GateSpec{ListenPort: 5432, BackendHost: hostGateway, BackendPort: backendPort, ClientCN: gateUID})
	must(t, err)
	gateDir := writeDir(t, map[string][]byte{CombinedPEMFile: server, CAFile: ca.pem, ConfigFile: []byte(gateConfig)})
	gate := startHAProxy(t, image, gateDir, false)
	gateIP := docker(t, "inspect", "-f", `{{(index .NetworkSettings.Networks "bridge").IPAddress}}`, gate)

	proxyConfig, err := ProxyConfig(gateIP, 5432, serverName)
	must(t, err)
	proxyOf := func(files map[string][]byte) string {
		files[ConfigFile] = []byte(proxyConfig)
		return startHAProxy(t, image, writeDir(t, files), true)
	}
	own := proxyOf(map[string][]byte{TenantPEMFile: ca.issue(t, 11, gateUID, 2, nil, nil), CAFile: ca.pem})
	otherCN := proxyOf(map[string][]byte{TenantPEMFile: ca.issue(t, 12, otherUID, 2, nil, nil), CAFile: ca.pem})
	otherCA := proxyOf(map[string][]byte{TenantPEMFile: strangerCA.issue(t, 13, gateUID, 2, nil, nil), CAFile: ca.pem})

	if line, err := through(t, docker(t, "port", own, "5432/tcp")); err != nil || line != "hello\n" {
		t.Fatalf("the proxy of the database was refused: %q %v", line, err)
	}
	if line, err := through(t, docker(t, "port", otherCN, "5432/tcp")); err == nil || line != "" {
		t.Fatalf("a proxy with the certificate of database B reached database A: %q %v", line, err)
	}
	if line, err := through(t, docker(t, "port", otherCA, "5432/tcp")); err == nil || line != "" {
		t.Fatalf("a proxy with a certificate of another CA reached the gate: %q %v", line, err)
	}
	t.Run("backend outages affect readiness only", func(t *testing.T) {
		for _, name := range []string{gate, own} {
			awaitHAProxyStatus(t, name, livenessPath, http.StatusOK)
			awaitHAProxyStatus(t, name, readinessPath, http.StatusOK)
			awaitHAProxyStatus(t, name, livenessPath+"/", http.StatusNotFound)
			awaitHAProxyStatus(t, name, readinessPath+"/", http.StatusNotFound)
		}
		must(t, backend.Close())
		awaitHAProxyStatus(t, gate, readinessPath, http.StatusServiceUnavailable)
		awaitHAProxyStatus(t, gate, livenessPath, http.StatusOK)
		docker(t, "stop", "-t", "2", gate)
		awaitHAProxyStatus(t, own, readinessPath, http.StatusServiceUnavailable)
		awaitHAProxyStatus(t, own, livenessPath, http.StatusOK)
	})
}
