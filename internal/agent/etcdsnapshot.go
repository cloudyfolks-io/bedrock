package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/schedule"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	etcdSnapshotPrefix = "etcd-"
	keptEtcdSnapshots  = 7
	etcdScheduleKey    = "backup.etcd-schedule"

	snapshotFailureFile  = ".etcd-snapshot-failure"
	firstSnapshotBackoff = time.Minute
	maxBackoffDoublings  = 20
)

func RunEtcdSnapshots(ctx context.Context, c client.Client, deps Deps) error {
	var own v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: deps.Node}, &own); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !v1alpha1.HostHasRole(own, v1alpha1.RoleControlPlane) {
		return nil
	}
	expr, err := etcdSchedule(ctx, c)
	if err != nil {
		return err
	}
	due, err := schedule.Due(expr, newestEtcdSnapshot(filepath.Join(deps.Root, backupDir)), deps.Now())
	if err != nil || !due {
		return err
	}
	dir := filepath.Join(deps.Root, backupDir)
	interval, err := schedule.MinInterval(expr, deps.Now())
	if err != nil {
		return err
	}
	failedAt, attempts := readSnapshotFailure(dir)
	if attempts > 0 && deps.Now().Before(failedAt.Add(etcdBackoff(attempts, interval))) {
		return nil
	}
	if err := takeSnapshot(ctx, c, deps, etcdSnapshotPrefix+deps.Now().UTC().Format(stampLayout)+sealedSuffix); err != nil {
		return errors.Join(err, recordSnapshotFailure(dir, deps.Now(), attempts+1))
	}
	return clearSnapshotFailure(dir)
}

func takeSnapshot(ctx context.Context, c client.Client, deps Deps, name string) error {
	recipients, err := sealRecipients(ctx, c, deps, name)
	if err != nil {
		return err
	}
	_, _, err = writeBackup(ctx, deps, name, etcdSnapshotPrefix, keptEtcdSnapshots, recipients)
	return err
}

func etcdBackoff(attempts int, interval time.Duration) time.Duration {
	if attempts > maxBackoffDoublings {
		return interval
	}
	return min(firstSnapshotBackoff<<(attempts-1), interval)
}

func readSnapshotFailure(dir string) (time.Time, int) {
	raw, err := os.ReadFile(filepath.Join(dir, snapshotFailureFile))
	if err != nil {
		return time.Time{}, 0
	}
	var seconds int64
	var attempts int
	if _, err := fmt.Sscanf(string(raw), "%d %d", &seconds, &attempts); err != nil {
		return time.Time{}, 0
	}
	return time.Unix(seconds, 0), attempts
}

func recordSnapshotFailure(dir string, at time.Time, attempts int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, snapshotFailureFile), []byte(fmt.Sprintf("%d %d", at.Unix(), attempts)), 0o600)
}

func clearSnapshotFailure(dir string) error {
	return removeIfExists(filepath.Join(dir, snapshotFailureFile))
}

func etcdSchedule(ctx context.Context, c client.Client) (string, error) {
	var setting v1alpha1.Setting
	err := c.Get(ctx, client.ObjectKey{Name: etcdScheduleKey}, &setting)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", err
	}
	if setting.Spec.Value != "" {
		return setting.Spec.Value, nil
	}
	def, _ := settings.Lookup(etcdScheduleKey)
	return def.Default, nil
}

func newestEtcdSnapshot(dir string) time.Time {
	archives, err := archivePaths(dir, etcdSnapshotPrefix, "")
	if err != nil {
		return time.Time{}
	}
	stamps := make([]time.Time, 0, len(archives))
	for _, archive := range archives {
		if stamp, err := time.Parse(stampLayout, backupStamp(archive)); err == nil {
			stamps = append(stamps, stamp)
		}
	}
	if len(stamps) == 0 {
		return time.Time{}
	}
	return slices.MaxFunc(stamps, func(a, b time.Time) int { return a.Compare(b) })
}
