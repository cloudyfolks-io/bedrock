package database

import (
	"context"
	"strings"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
	"github.com/cloudyfolks-io/bedrock/internal/database/valkey"
)

func primaryGVK(kind string) schema.GroupVersionKind {
	switch kind {
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.ClusterGVK
	case v1alpha1.DatabaseEngineValkey:
		return valkey.DeploymentGVK
	}
	return ClusterGVK
}

func renderFor(kind string, spec engine.Spec) ([]*unstructured.Unstructured, error) {
	switch kind {
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.Render(spec)
	case v1alpha1.DatabaseEngineValkey:
		return valkey.Render(spec)
	}
	return postgres.Render(spec)
}

func healthFor(kind string, obj *unstructured.Unstructured) engine.Health {
	switch kind {
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.Health(obj)
	case v1alpha1.DatabaseEngineValkey:
		return valkey.Health(obj)
	}
	return postgres.Health(obj)
}

func readyInstances(kind string, obj *unstructured.Unstructured) int32 {
	path := []string{"status", "readyInstances"}
	switch kind {
	case v1alpha1.DatabaseEngineMySQL:
		path = []string{"status", "mysql", "ready"}
	case v1alpha1.DatabaseEngineValkey:
		path = []string{"status", "readyReplicas"}
	}
	ready, _, _ := unstructured.NestedInt64(obj.Object, path...)
	return int32(ready)
}

func reportedImage(kind string, obj *unstructured.Unstructured) string {
	switch kind {
	case v1alpha1.DatabaseEngineMySQL:
		image, _, _ := unstructured.NestedString(obj.Object, "status", "mysql", "image")
		return image
	case v1alpha1.DatabaseEngineValkey:
		containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		for _, item := range containers {
			if container, ok := item.(map[string]any); ok && container["name"] == "valkey" {
				image, _ := container["image"].(string)
				return image
			}
		}
		return ""
	}
	image, _, _ := unstructured.NestedString(obj.Object, "status", "image")
	return image
}

func backupGVK(kind string) (schema.GroupVersionKind, bool) {
	switch kind {
	case v1alpha1.DatabaseEnginePostgreSQL:
		return BackupGVK, true
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.BackupGVK, true
	}
	return schema.GroupVersionKind{}, false
}

func backupClusterOf(obj *unstructured.Unstructured) string {
	if name, found, _ := unstructured.NestedString(obj.Object, "spec", "clusterName"); found {
		return name
	}
	name, _, _ := unstructured.NestedString(obj.Object, "spec", "cluster", "name")
	return name
}

type DirectoryLister struct {
	Store  BackupStore
	Reader client.Reader
}

func (l DirectoryLister) ListDirectories(ctx context.Context, bucket, prefix string) ([]string, error) {
	keys, err := BackupCredentials(ctx, l.Reader)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var directories []string
	token := ""
	for {
		page, err := l.Store.S3.ListObjectsV2(ctx, l.Store.Endpoint, l.Store.Region, keys, bucket, prefix, token)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Objects {
			name, _, nested := strings.Cut(strings.TrimPrefix(object.Key, prefix), "/")
			if nested && !seen[name] {
				seen[name] = true
				directories = append(directories, name)
			}
		}
		if !page.Truncated || page.NextToken == "" {
			return directories, nil
		}
		token = page.NextToken
	}
}

func (r *Reconciler) mysqlDestination(ctx context.Context, cfg Config, sourceUID string) (string, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(mysql.BackupGVK.GroupVersion().WithKind("PerconaServerMySQLBackupList"))
	if err := r.Live.List(ctx, list, client.InNamespace(PlatformNamespace)); err != nil {
		return "", client.IgnoreNotFound(err)
	}
	var own []unstructured.Unstructured
	for _, backup := range list.Items {
		if backupClusterOf(&backup) == sourceUID {
			own = append(own, backup)
		}
	}
	return mysql.ResolveDestination(ctx, own, DirectoryLister{Store: r.Store, Reader: r.Live}, cfg.BackupBucket, sourceUID)
}

func (r *Reconciler) expansionAllowed(ctx context.Context) (bool, error) {
	var class storagev1.StorageClass
	err := r.Live.Get(ctx, client.ObjectKey{Name: StorageClass}, &class)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil && class.AllowVolumeExpansion != nil && *class.AllowVolumeExpansion, err
}

func (r *Reconciler) upgradeTargets(ctx context.Context, db v1alpha1.Database, appliedVersion string) ([]string, error) {
	var current v1alpha1.DatabaseVersion
	err := r.Live.Get(ctx, client.ObjectKey{Name: db.Spec.Engine + "-" + appliedVersion}, &current)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var targets []string
	for _, major := range current.Spec.UpgradeTo {
		var target v1alpha1.DatabaseVersion
		err := r.Live.Get(ctx, client.ObjectKey{Name: db.Spec.Engine + "-" + major}, &target)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		if err == nil && target.Spec.Enabled {
			targets = append(targets, major)
		}
	}
	return targets, nil
}

func (r *Reconciler) operationFacts(ctx context.Context, in *flow) (OperationFacts, error) {
	db, applied := *in.db, *in.db.Status.Applied
	facts := OperationFacts{Class: in.catalog.Class.Spec}
	var err error
	if facts.ExpansionAllowed, err = r.expansionAllowed(ctx); err != nil {
		return facts, err
	}
	targets, err := r.upgradeTargets(ctx, db, applied.Version)
	if err != nil {
		return facts, err
	}
	backups, err := ListBackups(ctx, r.Live, db)
	if err != nil {
		return facts, err
	}
	upgrade := UpgradeBackupFacts(Name(db.UID), db.Spec.Version, r.now(), in.cfg.BackupMaxAge, backups, BackupStateOf(db.Spec.Engine))
	in.preBackups = FailedPreUpgradeBackups(Name(db.UID), backups, BackupStateOf(db.Spec.Engine))
	facts.Upgrade = UpgradeFacts{Targets: targets, LatestBackup: upgrade.Latest, PreRequested: upgrade.Requested, PreFailedAt: upgrade.FailedAt, MaxAge: in.cfg.BackupMaxAge, Now: r.now()}
	return facts, nil
}

func liveInstances(kind string, obj *unstructured.Unstructured) int {
	path := []string{"spec", "instances"}
	switch kind {
	case v1alpha1.DatabaseEngineMySQL:
		path = []string{"spec", "mysql", "size"}
	case v1alpha1.DatabaseEngineValkey:
		return 0
	}
	have, _, _ := unstructured.NestedInt64(obj.Object, path...)
	return int(have)
}

func appliedOf(db v1alpha1.Database) v1alpha1.DatabaseApplied {
	return v1alpha1.DatabaseApplied{
		Class: db.Spec.Class, Version: db.Spec.Version, StorageGi: db.Spec.StorageGi, Suspended: db.Spec.Suspended,
		RestartToken: db.Annotations[v1alpha1.AnnotationRestart],
	}
}

func hasStep(steps []Step, want Step) bool {
	for _, step := range steps {
		if step == want {
			return true
		}
	}
	return false
}

func imageFor(db v1alpha1.Database, catalogImage string, plan Result) string {
	appliedVersion := db.Spec.Version
	if db.Status.Applied != nil {
		appliedVersion = db.Status.Applied.Version
	}
	return ImageToApply(db.Status.Image, appliedVersion, db.Spec.Version, catalogImage, hasStep(plan.Steps, StepRefreshImage))
}
