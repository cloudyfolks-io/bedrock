package images

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var (
	DataVolumeGVK     = schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume"}
	DataImportCronGVK = schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataImportCron"}
	DataSourceGVK     = schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataSource"}
	VolumeSnapshotGVK = schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshot"}
)

const annotationBindImmediate = "cdi.kubevirt.io/storage.bind.immediate.requested"

func DataImportCron(s Subject) *unstructured.Unstructured {
	return object(DataImportCronGVK, s, s.Name, nil, map[string]any{
		"schedule":          s.Spec.Refresh.Schedule,
		"managedDataSource": s.Name,
		"garbageCollect":    "Outdated",
		"importsToKeep":     int64(Keep(s.Spec)),
		"retentionPolicy":   "None",
		"template": map[string]any{
			"spec": map[string]any{
				"source":  map[string]any{"registry": registrySource(*s.Spec.Source.Registry)},
				"storage": storageOf(SizeGi(s.Spec)),
			},
		},
	})
}

func ImportDataVolume(s Subject, sizeGi int32) *unstructured.Unstructured {
	annotations := withAttach(s, map[string]string{
		annotationBindImmediate: "true",
		AnnotationSourceHash:    SourceHash(s.Spec),
	})
	return object(DataVolumeGVK, s, ImportName(s.Name), annotations, map[string]any{
		"source":  importSource(s),
		"storage": storageOf(sizeGi),
	})
}

func GoldenSnapshot(s Subject, claim, digest string) *unstructured.Unstructured {
	annotations := withAttach(s, map[string]string{
		AnnotationSourceHash: SourceHash(s.Spec),
		AnnotationDigest:     digest,
	})
	return object(VolumeSnapshotGVK, s, goldenNameOf(s, digest), annotations, map[string]any{
		"volumeSnapshotClassName": SnapshotClass,
		"source":                  map[string]any{"persistentVolumeClaimName": claim},
	})
}

func DataSource(s Subject, name, snapshot string) *unstructured.Unstructured {
	return object(DataSourceGVK, s, name, withAttach(s, nil), map[string]any{
		"source": map[string]any{
			"snapshot": map[string]any{"name": snapshot, "namespace": s.Namespace},
		},
	})
}

func object(gvk schema.GroupVersionKind, s Subject, name string, annotations map[string]string, spec map[string]any) *unstructured.Unstructured {
	meta := map[string]any{
		"name":      name,
		"namespace": s.Namespace,
		"labels":    stringMap(s.Labels),
		"ownerReferences": []any{map[string]any{
			"apiVersion":         s.Owner.APIVersion,
			"kind":               s.Owner.Kind,
			"name":               s.Owner.Name,
			"uid":                string(s.Owner.UID),
			"controller":         true,
			"blockOwnerDeletion": true,
		}},
	}
	if len(annotations) > 0 {
		meta["annotations"] = stringMap(annotations)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata":   meta,
		"spec":       spec,
	}}
}

func withAttach(s Subject, annotations map[string]string) map[string]string {
	out := make(map[string]string, len(annotations)+1)
	for k, v := range annotations {
		out[k] = v
	}
	if ReadOnlyAttach(s.Spec) {
		out[AnnotationAttach] = AttachReadOnly
	}
	return out
}

func stringMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func importSource(s Subject) map[string]any {
	source := s.Spec.Source
	switch {
	case source.Registry != nil:
		return map[string]any{"registry": registrySource(*source.Registry)}
	case source.HTTP != nil:
		return map[string]any{"http": httpSource(s, *source.HTTP)}
	case source.VolumeRef != nil:
		return map[string]any{"pvc": map[string]any{"namespace": s.Namespace, "name": source.VolumeRef.Name}}
	default:
		return map[string]any{"upload": map[string]any{}}
	}
}

func registrySource(r v1alpha1.ImageRegistrySource) map[string]any {
	if r.PullSecretRef == "" {
		return map[string]any{"url": "docker://" + r.Image, "pullMethod": "node"}
	}
	return map[string]any{"url": "docker://" + untag(r.Image), "pullMethod": "pod", "secretRef": r.PullSecretRef}
}

func untag(image string) string {
	repository, digest, pinned := strings.Cut(image, "@")
	if !pinned {
		return image
	}
	if colon := strings.LastIndex(repository, ":"); colon > strings.LastIndex(repository, "/") {
		repository = repository[:colon]
	}
	return repository + "@" + digest
}

func httpSource(s Subject, h v1alpha1.ImageHTTPSource) map[string]any {
	source := map[string]any{"url": h.URL}
	if h.Checksum != "" {
		source["checksum"] = h.Checksum
	}
	if h.SecretRef != "" {
		source["secretRef"] = h.SecretRef
	}
	if cert := certConfigMap(s, h); cert != "" {
		source["certConfigMap"] = cert
	}
	return source
}

func certConfigMap(s Subject, h v1alpha1.ImageHTTPSource) string {
	switch {
	case h.CAConfigMap != "":
		return h.CAConfigMap
	case s.ExtraCA:
		return AdditionalCAConfigMap
	default:
		return ""
	}
}

func storageOf(sizeGi int32) map[string]any {
	return map[string]any{
		"storageClassName": StorageClass,
		"resources":        map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", sizeGi)}},
	}
}
