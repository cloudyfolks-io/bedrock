package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/s3"
	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
	"github.com/cloudyfolks-io/bedrock/internal/backup/target"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	shipStateFile = "var/lib/bedrock/etcd-ship.json"
	shipMetricDir = "var/lib/node_exporter/textfile_collector"
	shipMetric    = "bedrock_backup.prom"
	shipKeyDir    = "etcd"

	targetKey      = "backup.target"
	credentialsKey = "backup.credentials-secret"

	ReasonShipNoRecipients  = "ArchiveRecipientsMissing"
	ReasonShipTargetInvalid = "BackupTargetInvalid"
	ReasonShipCredentials   = "BackupCredentialsInvalid"
	ReasonShipUploadFailed  = "UploadFailed"
)

type shipState struct {
	LastUploadUnix int64  `json:"lastUploadUnix,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Message        string `json:"message,omitempty"`
}

func RunEtcdShip(ctx context.Context, c client.Client, deps Deps) error {
	var own v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: deps.Node}, &own); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !v1alpha1.HostHasRole(own, v1alpha1.RoleControlPlane) {
		return nil
	}
	previous := readShipState(deps.Root)
	rawTarget, err := settingValue(ctx, c, targetKey)
	if err != nil {
		return err
	}
	if rawTarget == "" {
		return removeShipState(deps.Root)
	}
	next := shipOnce(ctx, c, deps, rawTarget, previous)
	return errors.Join(writeShipState(deps.Root, next), writeShipMetric(deps.Root, next))
}

func shipOnce(ctx context.Context, c client.Client, deps Deps, rawTarget string, previous shipState) shipState {
	failed := func(reason string, err error) shipState {
		return shipState{LastUploadUnix: previous.LastUploadUnix, Reason: reason, Message: err.Error()}
	}
	recipients, err := archiveRecipientsValue(ctx, c)
	if err != nil {
		return failed(ReasonShipUploadFailed, err)
	}
	if _, err := seal.ParseRecipients(recipients); err != nil || strings.TrimSpace(recipients) == "" {
		return failed(ReasonShipNoRecipients, errors.New("set "+archiveRecipientsKey+" to a valid age recipient: archives are not uploaded without an admin recipient"))
	}
	tgt, err := target.Parse(rawTarget)
	if err != nil {
		return failed(ReasonShipTargetInvalid, err)
	}
	s3client, reason, err := shipClient(ctx, c, deps, tgt)
	if err != nil {
		return failed(reason, err)
	}
	keyDir := path.Join(tgt.Prefix, shipKeyDir, deps.Node)
	uploaded, err := syncArchives(ctx, s3client, filepath.Join(deps.Root, backupDir), keyDir, keptEtcdSnapshots)
	if err != nil {
		return failed(ReasonShipUploadFailed, err)
	}
	if uploaded > 0 {
		return shipState{LastUploadUnix: deps.Now().Unix()}
	}
	return shipState{LastUploadUnix: previous.LastUploadUnix}
}

func shipClient(ctx context.Context, c client.Client, deps Deps, tgt target.Target) (s3.Client, string, error) {
	name, err := settingValue(ctx, c, credentialsKey)
	if err != nil {
		return s3.Client{}, ReasonShipUploadFailed, err
	}
	var secret corev1.Secret
	err = c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &secret)
	if name == "" || apierrors.IsNotFound(err) {
		return s3.Client{}, ReasonShipCredentials, fmt.Errorf("secret %s/%s does not exist: set %s", release.SystemNamespace, name, credentialsKey)
	}
	if err != nil {
		return s3.Client{}, ReasonShipUploadFailed, err
	}
	access, key := string(secret.Data["access-key-id"]), string(secret.Data["secret-access-key"])
	if access == "" || key == "" {
		return s3.Client{}, ReasonShipCredentials, fmt.Errorf("secret %s lacks access-key-id or secret-access-key", name)
	}
	httpClient, err := shipHTTP(deps, secret.Data["ca.crt"])
	if err != nil {
		return s3.Client{}, ReasonShipCredentials, err
	}
	return s3.Client{
		Endpoint: target.S3URL(tgt), Region: tgt.Region, Bucket: tgt.Bucket,
		AccessKey: access, SecretKey: key, HTTP: httpClient, Now: deps.Now, Sleep: time.Sleep,
	}, "", nil
}

func shipHTTP(deps Deps, ca []byte) (*http.Client, error) {
	base := deps.HTTP
	if base == nil {
		base = &http.Client{Timeout: 10 * time.Minute}
	}
	if len(ca) == 0 {
		return base, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("ca.crt holds no valid certificate")
	}
	return &http.Client{Timeout: base.Timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}, nil
}

func syncArchives(ctx context.Context, s3client s3.Client, dir, keyDir string, keep int) (int, error) {
	local, err := filepath.Glob(filepath.Join(dir, etcdSnapshotPrefix+"*"+sealedSuffix))
	if err != nil {
		return 0, err
	}
	remoteKeys, err := s3client.List(ctx, keyDir+"/")
	if err != nil {
		return 0, err
	}
	remote := make(map[string]bool, len(remoteKeys))
	for _, key := range remoteKeys {
		remote[path.Base(key)] = true
	}
	uploaded := 0
	for _, file := range local {
		name := filepath.Base(file)
		if remote[name] {
			continue
		}
		if err := s3client.Put(ctx, path.Join(keyDir, name), file); err != nil {
			return uploaded, err
		}
		remote[name] = true
		uploaded++
	}
	return uploaded, pruneRemote(ctx, s3client, keyDir, remote, keep)
}

func pruneRemote(ctx context.Context, s3client s3.Client, keyDir string, remote map[string]bool, keep int) error {
	names := make([]string, 0, len(remote))
	for name := range remote {
		if strings.HasPrefix(name, etcdSnapshotPrefix) && strings.HasSuffix(name, sealedSuffix) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, old := range names[:max(0, len(names)-keep)] {
		if err := s3client.Delete(ctx, path.Join(keyDir, old)); err != nil {
			return err
		}
	}
	return nil
}

func shipCondition(deps Deps, generation int64) []metav1.Condition {
	state := readShipState(deps.Root)
	if state.Reason == "" {
		return nil
	}
	return []metav1.Condition{condition(v1alpha1.ConditionEtcdOffClusterCopy, metav1.ConditionFalse, state.Reason, state.Message, generation)}
}

func readShipState(root string) shipState {
	raw, err := os.ReadFile(filepath.Join(root, shipStateFile))
	if err != nil {
		return shipState{}
	}
	var state shipState
	if json.Unmarshal(raw, &state) != nil {
		return shipState{}
	}
	return state
}

func writeShipState(root string, state shipState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file := filepath.Join(root, shipStateFile)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, raw, 0o644)
}

func writeShipMetric(root string, state shipState) error {
	dir := filepath.Join(root, shipMetricDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("# TYPE bedrock_backup_last_upload_timestamp_seconds gauge\nbedrock_backup_last_upload_timestamp_seconds{kind=\"etcd\"} %d\n", state.LastUploadUnix)
	return writeAtomic(filepath.Join(dir, shipMetric), func(w io.Writer) error {
		_, err := io.WriteString(w, body)
		return err
	})
}

func removeShipState(root string) error {
	return errors.Join(
		removeIfPresent(filepath.Join(root, shipStateFile)),
		removeIfPresent(filepath.Join(root, shipMetricDir, shipMetric)),
	)
}

func settingValue(ctx context.Context, c client.Client, key string) (string, error) {
	var setting v1alpha1.Setting
	if err := c.Get(ctx, client.ObjectKey{Name: key}, &setting); client.IgnoreNotFound(err) != nil {
		return "", err
	}
	return setting.Spec.Value, nil
}
