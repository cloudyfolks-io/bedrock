package agent

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/schedule"
)

const (
	etcdDefaultEndpoint   = "https://127.0.0.1:2379"
	etcdPKIDir            = "var/lib/k0s/pki"
	etcdClientCert        = "apiserver-etcd-client.crt"
	etcdClientKey         = "apiserver-etcd-client.key"
	etcdCA                = "etcd/ca.crt"
	etcdDefragStateFile   = "var/lib/bedrock/etcd-defrag"
	minReclaimBytes       = 268435456
	defragSlotMinutes     = 15
	defragSlotCount       = 4
	defragRequestTimeout  = 10 * time.Minute
	etcdProbeTimeout      = 30 * time.Second
	etcdStatusPath        = "/v3/maintenance/status"
	etcdDefragmentPath    = "/v3/maintenance/defragment"
	etcdHealthPath        = "/health"
	etcdEmptyJSONObject   = "{}"
	etcdHealthyAnswerWord = "true"
)

type etcdStatus struct {
	DBSize      int64
	DBSizeInUse int64
}

func defragSchedule(index int) string {
	return fmt.Sprintf("%d 3 * * 0", defragSlotMinutes*(index%defragSlotCount))
}

func controllerIndex(hosts []v1alpha1.Host, node string) (int, bool) {
	names := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if v1alpha1.HostHasRole(h, v1alpha1.RoleControlPlane) {
			names = append(names, h.Name)
		}
	}
	slices.Sort(names)
	index := slices.Index(names, node)
	return index, index >= 0
}

func defragWorthIt(status etcdStatus) bool {
	return status.DBSize-status.DBSizeInUse >= minReclaimBytes
}

func parseEtcdStatus(raw []byte) (etcdStatus, error) {
	var wire struct {
		DBSize      json.Number `json:"dbSize"`
		DBSizeInUse json.Number `json:"dbSizeInUse"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		return etcdStatus{}, err
	}
	size, err := sizeOf(wire.DBSize)
	if err != nil {
		return etcdStatus{}, fmt.Errorf("dbSize: %w", err)
	}
	inUse, err := sizeOf(wire.DBSizeInUse)
	if err != nil {
		return etcdStatus{}, fmt.Errorf("dbSizeInUse: %w", err)
	}
	return etcdStatus{DBSize: size, DBSizeInUse: inUse}, nil
}

func sizeOf(value json.Number) (int64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(string(value), 10, 64)
}

func etcdCall(ctx context.Context, c *http.Client, method, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, defragRequestTimeout)
	defer cancel()
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(etcdEmptyJSONObject)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

func etcdMemberHealth(ctx context.Context, c *http.Client, endpoint string) error {
	probe, cancel := context.WithTimeout(ctx, etcdProbeTimeout)
	defer cancel()
	raw, err := etcdCall(probe, c, http.MethodGet, endpoint+etcdHealthPath)
	if err != nil {
		return err
	}
	var answer struct {
		Health string `json:"health"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return err
	}
	if answer.Health != etcdHealthyAnswerWord {
		return fmt.Errorf("etcd member at %s is not healthy: %s", endpoint, strings.TrimSpace(string(raw)))
	}
	return nil
}

func defragMember(ctx context.Context, c *http.Client, endpoint string) (bool, error) {
	if err := etcdMemberHealth(ctx, c, endpoint); err != nil {
		return false, err
	}
	raw, err := etcdCall(ctx, c, http.MethodPost, endpoint+etcdStatusPath)
	if err != nil {
		return false, err
	}
	status, err := parseEtcdStatus(raw)
	if err != nil {
		return false, err
	}
	if !defragWorthIt(status) {
		return false, nil
	}
	if _, err := etcdCall(ctx, c, http.MethodPost, endpoint+etcdDefragmentPath); err != nil {
		return true, err
	}
	return true, etcdMemberHealth(ctx, c, endpoint)
}

func etcdHTTPClient(root string) (*http.Client, error) {
	dir := filepath.Join(root, etcdPKIDir)
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, etcdClientCert), filepath.Join(dir, etcdClientKey))
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, etcdCA))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no certificate in %s", etcdCA)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, MinVersion: tls.VersionTLS12}}}, nil
}

func readDefragStamp(root string) time.Time {
	raw, err := os.ReadFile(filepath.Join(root, etcdDefragStateFile))
	if err != nil {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func writeDefragStamp(root string, at time.Time) error {
	path := filepath.Join(root, etcdDefragStateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(at.Unix(), 10)), 0o600)
}

func RunEtcdDefrag(ctx context.Context, c client.Client, deps Deps) error {
	var hosts v1alpha1.HostList
	if err := c.List(ctx, &hosts); err != nil {
		return err
	}
	index, ok := controllerIndex(hosts.Items, deps.Node)
	if !ok {
		return nil
	}
	last := readDefragStamp(deps.Root)
	if last.IsZero() {
		return writeDefragStamp(deps.Root, deps.Now())
	}
	due, err := schedule.Due(defragSchedule(index), last, deps.Now())
	if err != nil || !due {
		return err
	}
	httpClient, err := etcdHTTPClient(deps.Root)
	if err != nil {
		return err
	}
	endpoint := cmp.Or(deps.EtcdEndpoint, etcdDefaultEndpoint)
	did, defragErr := defragMember(ctx, httpClient, endpoint)
	if defragErr != nil && !did {
		return defragErr
	}
	if did && defragErr == nil {
		logLine("etcd member defragmented")
	}
	return errors.Join(defragErr, writeDefragStamp(deps.Root, deps.Now()))
}
