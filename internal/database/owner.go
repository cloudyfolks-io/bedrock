package database

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

var (
	Finalizer = platform.Finalizer(Kind)

	ClusterGVK         = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	ScheduledBackupGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "ScheduledBackup"}
	BackupGVK          = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Backup"}
	ObjectStoreGVK     = schema.GroupVersionKind{Group: "barmancloud.cnpg.io", Version: "v1", Kind: "ObjectStore"}
	ServiceGVK         = schema.GroupVersionKind{Version: "v1", Kind: "Service"}
	SecretGVK          = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}
	DeploymentGVK      = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	NetworkPolicyGVK   = schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}
	CertificateGVK     = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	PersistentClaimGVK = schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"}
	ConfigMapGVK       = schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	PodGVK             = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	NamespaceGVK       = schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}
)

func EngineGVKs() []schema.GroupVersionKind {
	return []schema.GroupVersionKind{
		ScheduledBackupGVK, ClusterGVK, ObjectStoreGVK,
		DeploymentGVK, ServiceGVK, NetworkPolicyGVK, CertificateGVK, SecretGVK,
		ConfigMapGVK, PodGVK, PersistentClaimGVK,
	}
}

type Project struct {
	Namespace string
	Tenant    string
	Name      string
}

func ProjectOf(ctx context.Context, c client.Reader, namespace string) (Project, bool, error) {
	ns := &metav1.PartialObjectMetadata{}
	ns.SetGroupVersionKind(NamespaceGVK)
	if err := c.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return Project{}, false, client.IgnoreNotFound(err)
	}
	labels := ns.GetLabels()
	project := Project{Namespace: namespace, Tenant: labels[v1alpha1.LabelTenant], Name: labels[v1alpha1.LabelProject]}
	return project, project.Tenant != "" && project.Name != "", nil
}

func OwnerOf(db v1alpha1.Database, project Project) platform.Owner {
	return platform.Owner{Kind: Kind, Name: db.Name, Namespace: db.Namespace, UID: db.UID, Tenant: project.Tenant, Project: project.Name}
}

func Adopt(obj *unstructured.Unstructured, db v1alpha1.Database, project Project) *unstructured.Unstructured {
	adopted := platform.Adopt(obj, OwnerOf(db, project))
	labels := adopted.GetLabels()
	labels[LabelManagedBy] = ManagedBy
	labels[LabelInstance] = Name(db.UID)
	adopted.SetLabels(labels)
	return adopted
}

func OwnedMetadata(db v1alpha1.Database, project Project) (map[string]string, map[string]string) {
	probe := Adopt(&unstructured.Unstructured{Object: map[string]any{}}, db, project)
	return probe.GetLabels(), probe.GetAnnotations()
}

func InstanceSelector(db v1alpha1.Database) client.MatchingLabels {
	return client.MatchingLabels{LabelManagedBy: ManagedBy, LabelInstance: Name(db.UID)}
}

func OwnerExists(c client.Reader) func(context.Context, platform.Owner) (bool, error) {
	return func(ctx context.Context, owner platform.Owner) (bool, error) {
		if owner.Kind != Kind || owner.UID == "" {
			return true, nil
		}
		var db v1alpha1.Database
		err := c.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: owner.Name}, &db)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return db.UID == owner.UID, nil
	}
}

func EnsureFinalizer(ctx context.Context, c client.Client, db *v1alpha1.Database) error {
	return patchFinalizer(ctx, c, db, controllerutil.AddFinalizer)
}

func ReleaseFinalizer(ctx context.Context, c client.Client, db *v1alpha1.Database) error {
	return patchFinalizer(ctx, c, db, controllerutil.RemoveFinalizer)
}

func patchFinalizer(ctx context.Context, c client.Client, db *v1alpha1.Database, change func(client.Object, string) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(db), db); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := db.DeepCopy()
		if !change(db, Finalizer) {
			return nil
		}
		return c.Patch(ctx, db, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}

func EngineObjects(ctx context.Context, c client.Reader, db v1alpha1.Database) ([]*unstructured.Unstructured, error) {
	var found []*unstructured.Unstructured
	for _, gvk := range EngineGVKs() {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		err := c.List(ctx, list, client.InNamespace(PlatformNamespace), InstanceSelector(db))
		if meta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			found = append(found, &list.Items[i])
		}
	}
	return found, nil
}

func DeleteEngineObjects(ctx context.Context, c client.Client, db v1alpha1.Database) (bool, error) {
	found, err := EngineObjects(ctx, c, db)
	if err != nil {
		return false, err
	}
	for _, obj := range found {
		if obj.GetDeletionTimestamp().IsZero() {
			if err := c.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
				return false, err
			}
		}
	}
	return len(found) == 0, nil
}
