package agent

import (
	"context"
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
	etcdSnapshotPattern = "etcd-*.tar.gz"
	keptEtcdSnapshots   = 7
	etcdScheduleKey     = "backup.etcd-schedule"
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
	name := "etcd-" + deps.Now().UTC().Format(stampLayout) + ".tar.gz"
	_, _, err = writeBackup(ctx, deps, name, etcdSnapshotPattern, keptEtcdSnapshots)
	return err
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
	archives, err := filepath.Glob(filepath.Join(dir, etcdSnapshotPattern))
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
