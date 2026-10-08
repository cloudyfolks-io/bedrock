package images

import (
	"context"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const labelDataImportCron = "cdi.kubevirt.io/dataImportCron"

func PlatformHost(ctx context.Context, c client.Reader) (string, error) {
	return settingValue(ctx, c, "platform.host")
}

func settingValue(ctx context.Context, c client.Reader, name string) (string, error) {
	var setting v1alpha1.Setting
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &setting); err != nil {
		return "", client.IgnoreNotFound(err)
	}
	return setting.Spec.Value, nil
}

func Observe(ctx context.Context, c client.Reader, s Subject, prev v1alpha1.ImageStatus, host string) (World, error) {
	world := World{PlatformHost: host, Prev: prev}
	cron, err := getOptional(ctx, c, DataImportCronGVK, s.Namespace, s.Name)
	if err != nil {
		return World{}, err
	}
	world.Cron = cron
	importName := importNameOf(s, cron)
	if world.DataVolume, err = getOptional(ctx, c, DataVolumeGVK, s.Namespace, importName); err != nil {
		return World{}, err
	}
	if world.ImportClaim, err = getClaim(ctx, c, s.Namespace, importName); err != nil {
		return World{}, err
	}
	if ref := s.Spec.Source.VolumeRef; ref != nil {
		if world.SourceClaim, err = getClaim(ctx, c, s.Namespace, ref.Name); err != nil {
			return World{}, err
		}
	}
	if world.Snapshots, err = observeSnapshots(ctx, c, s); err != nil {
		return World{}, err
	}
	if world.DataSources, err = observeDataSources(ctx, c, s); err != nil {
		return World{}, err
	}
	return world, nil
}

func importNameOf(s Subject, cron *unstructured.Unstructured) string {
	if cron != nil {
		if name, _, found := CronImport(cron); found {
			return name
		}
	}
	return ImportName(s.Name)
}

func observeSnapshots(ctx context.Context, c client.Reader, s Subject) ([]unstructured.Unstructured, error) {
	owned, err := listLabelled(ctx, c, VolumeSnapshotGVK, s.Namespace, ownedLabels(s))
	if err != nil {
		return nil, err
	}
	scheduled, err := listLabelled(ctx, c, VolumeSnapshotGVK, s.Namespace, map[string]string{labelDataImportCron: s.Name})
	if err != nil {
		return nil, err
	}
	return unionByName(owned, scheduled), nil
}

func observeDataSources(ctx context.Context, c client.Reader, s Subject) ([]unstructured.Unstructured, error) {
	owned, err := listLabelled(ctx, c, DataSourceGVK, s.Namespace, ownedLabels(s))
	if err != nil {
		return nil, err
	}
	alias, err := getOptional(ctx, c, DataSourceGVK, s.Namespace, s.Name)
	if err != nil || alias == nil {
		return owned, err
	}
	return unionByName(owned, []unstructured.Unstructured{*alias}), nil
}

func ownedLabels(s Subject) map[string]string {
	return map[string]string{
		v1alpha1.LabelKind: s.Labels[v1alpha1.LabelKind],
		v1alpha1.LabelName: s.Labels[v1alpha1.LabelName],
	}
}

func unionByName(lists ...[]unstructured.Unstructured) []unstructured.Unstructured {
	var union []unstructured.Unstructured
	for _, list := range lists {
		for _, item := range list {
			if !slices.ContainsFunc(union, func(have unstructured.Unstructured) bool { return have.GetName() == item.GetName() }) {
				union = append(union, item)
			}
		}
	}
	return union
}

func getOptional(ctx context.Context, c client.Reader, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return obj, nil
}

func getClaim(ctx context.Context, c client.Reader, namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	var claim corev1.PersistentVolumeClaim
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &claim); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &claim, nil
}

func listLabelled(ctx context.Context, c client.Reader, gvk schema.GroupVersionKind, namespace string, labels map[string]string) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := c.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels(labels)); err != nil {
		return nil, err
	}
	return list.Items, nil
}
